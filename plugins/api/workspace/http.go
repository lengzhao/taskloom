package workspace

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/security"
	api "github.com/lengzhao/taskloom/cap/workspace"
)

//go:embed app.html
var assets embed.FS

type Config struct {
	Origin    string `json:"origin"`
	LocalHTTP bool   `json:"localHTTP"`
}
type Deps struct {
	Commands api.Commands            `json:"commands"`
	Queries  api.Queries             `json:"queries"`
	Changes  api.ChangeFeed          `json:"changes"`
	Identity identity.Authenticator  `json:"identity"`
	Security security.MemberCommands `json:"security"`
}
type Module struct {
	c        Config
	d        Deps
	aead     cipher.AEAD
	mu       sync.Mutex
	sessions map[string]int64
}

func init() { pluginkit.Register("api/workspace", New) }
func New(c Config, d Deps) (*Module, error) {
	if c.Origin == "" || d.Commands == nil || d.Queries == nil || d.Changes == nil || d.Identity == nil || d.Security == nil {
		return nil, errors.New("workspace origin and capabilities required")
	}
	if c.LocalHTTP && c.Origin != "http://127.0.0.1:8080" && c.Origin != "http://localhost:8080" {
		return nil, errors.New("localHTTP restricted to loopback origins")
	}
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &Module{c: c, d: d, aead: aead, sessions: map[string]int64{}}, nil
}
func (m *Module) Routes() []httpapi.Route {
	routes := []httpapi.Route{}
	for _, r := range []struct{ method, path, op string }{{"GET", "/app", "ui.app"}, {"POST", "/session", "session.login"}, {"DELETE", "/session", "session.logout"}, {"GET", "/tasks", "tasks.list"}, {"POST", "/tasks", "tasks.create"}, {"GET", "/tasks/{id}", "tasks.get"}, {"PATCH", "/tasks/{id}", "tasks.edit"}, {"POST", "/tasks/{id}/commits", "commits.append"},
		{"POST", "/tasks/{id}/participants", "participants.set"}, {"GET", "/changes", "changes.read"}, {"POST", "/members", "members.set"}, {"DELETE", "/members", "members.remove"}} {
		routes = append(routes, httpapi.Route{Method: r.method, Path: r.path, Operation: r.op, Handler: http.HandlerFunc(m.serve)})
	}
	return routes
}

type session struct {
	Token   string
	Expires int64
}

func (m *Module) token(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	cookie, e := r.Cookie("taskloom_session")
	if e != nil {
		return ""
	}
	m.mu.Lock()
	expires, active := m.sessions[cookie.Value]
	m.mu.Unlock()
	if !active || expires < time.Now().Unix() {
		return ""
	}
	b, e := base64.RawURLEncoding.DecodeString(cookie.Value)
	if e != nil || len(b) < m.aead.NonceSize() {
		return ""
	}
	plain, e := m.aead.Open(nil, b[:m.aead.NonceSize()], b[m.aead.NonceSize():], nil)
	if e != nil {
		return ""
	}
	var s session
	if json.Unmarshal(plain, &s) != nil || s.Expires < time.Now().Unix() {
		return ""
	}
	return s.Token
}
func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	if origin := r.Header.Get("Origin"); origin != "" && origin != m.c.Origin {
		http.Error(w, "origin rejected", 403)
		return
	}
	path := r.URL.Path
	if strings.HasSuffix(path, "/app") {
		b, _ := assets.ReadFile("app.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
		return
	}
	if r.Method != "GET" && r.Header.Get("Authorization") == "" && r.Header.Get("X-Taskloom-Request") != "1" {
		http.Error(w, "request header required", 403)
		return
	}
	token := m.token(r)
	if strings.HasSuffix(path, "/session") {
		if r.Method == "DELETE" {
			if cookie, e := r.Cookie("taskloom_session"); e == nil {
				m.mu.Lock()
				delete(m.sessions, cookie.Value)
				m.mu.Unlock()
			}
			http.SetCookie(w, &http.Cookie{Name: "taskloom_session", Value: "", Path: "/", HttpOnly: true, Secure: !m.c.LocalHTTP, SameSite: http.SameSiteStrictMode, MaxAge: -1})
			write(w, map[string]bool{"ok": true})
			return
		}
		if _, e := m.d.Identity.Authenticate(r.Context(), token); e != nil {
			fail(w, e)
			return
		}
		plain, _ := json.Marshal(session{token, time.Now().Add(time.Hour).Unix()})
		nonce := make([]byte, m.aead.NonceSize())
		if _, e := rand.Read(nonce); e != nil {
			fail(w, e)
			return
		}
		sealed := m.aead.Seal(nonce, nonce, plain, nil)
		value := base64.RawURLEncoding.EncodeToString(sealed)
		m.mu.Lock()
		for k, expires := range m.sessions {
			if expires < time.Now().Unix() {
				delete(m.sessions, k)
			}
		}
		if len(m.sessions) >= 10000 {
			m.mu.Unlock()
			fail(w, errors.New("session capacity reached"))
			return
		}
		m.sessions[value] = time.Now().Add(time.Hour).Unix()
		m.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "taskloom_session", Value: value, Path: "/", HttpOnly: true, Secure: !m.c.LocalHTTP, SameSite: http.SameSiteStrictMode, MaxAge: 3600})
		write(w, map[string]bool{"ok": true})
		return
	}
	ctx := r.Context()
	key := r.Header.Get("Idempotency-Key")
	taskID := r.PathValue("id")
	var result any
	var err error
	switch {
	case strings.HasSuffix(path, "/changes"):
		result, err = m.d.Changes.Changes(ctx, token, r.URL.Query().Get("cursor"))
	case strings.HasSuffix(path, "/members"):
		var in authorization.Member
		if !decode(w, r, &in) {
			return
		}
		if r.Method == "DELETE" {
			in.Active = false
		}
		err = m.d.Security.SetMember(ctx, token, key, in)
		result = map[string]bool{"ok": err == nil}
	case strings.HasSuffix(path, "/participants"):
		var in api.Participation
		if !decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		err = m.d.Commands.SetParticipant(ctx, token, key, in)
		result = map[string]bool{"ok": err == nil}
	case strings.HasSuffix(path, "/commits"):
		var in api.Comment
		if !decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Commands.Comment(ctx, token, key, in)
	case taskID != "" && r.Method == "GET":
		after := int64(0)
		if raw := r.URL.Query().Get("after"); raw != "" {
			after, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || after < 0 {
				fail(w, fault.ErrInput)
				return
			}
		}
		result, err = m.d.Queries.Get(ctx, token, taskID, after)
	case taskID != "" && r.Method == "PATCH":
		var in api.Edit
		if !decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Commands.Edit(ctx, token, key, in)
	case r.Method == "GET":
		result, err = m.d.Queries.List(ctx, token)
	case r.Method == "POST":
		var in api.Create
		if !decode(w, r, &in) {
			return
		}
		result, err = m.d.Commands.Create(ctx, token, key, in)
	}
	if err != nil {
		fail(w, err)
		return
	}
	write(w, result)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		fail(w, fault.ErrInput)
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(w, fault.ErrInput)
		return false
	}
	return true
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	status := 500
	msg := "service unavailable"
	switch {
	case errors.Is(e, identity.ErrUnauthenticated):
		status = 401
		msg = "请重新登录"
	case errors.Is(e, authorization.ErrForbidden):
		status = 403
		msg = "无权执行此操作"
	case errors.Is(e, fault.ErrNotFound):
		status = 404
		msg = "记录不存在"
	case errors.Is(e, fault.ErrConflict):
		status = 409
		msg = "版本或请求冲突，请刷新"
	case errors.Is(e, fault.ErrInput):
		status = 400
		msg = "输入或游标无效，请刷新同步"
	}
	http.Error(w, msg, status)
}

func (m *Module) Open(context.Context) error { return nil }
func (m *Module) Close(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.sessions)
	return nil
}
