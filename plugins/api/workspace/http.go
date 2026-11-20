package workspace

import (
	"context"
	"embed"
	"errors"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/runtime/httpx"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/security"
	api "github.com/lengzhao/taskloom/cap/workspace"
)

//go:embed app.html
var assets embed.FS

// Origin and CSRF checks belong to the HTTP transport; this module only needs
// the tenant to log into when one database holds several.
type Config struct {
	LoginTenant string `json:"loginTenant"`
}

type Deps struct {
	Catalog  catalog.Service         `json:"catalog"`
	Commands api.Commands            `json:"commands"`
	Queries  api.Queries             `json:"queries"`
	Changes  api.ChangeFeed          `json:"changes"`
	Identity identity.Authenticator  `json:"identity"`
	Security security.Administration `json:"security"`
}
type Module struct {
	c             Config
	d             Deps
	mu            sync.Mutex
	loginAttempts map[string]loginWindow
}

func init() { pluginkit.Register("api/workspace", New) }
func New(c Config, d Deps) (*Module, error) {
	if d.Catalog == nil || d.Commands == nil || d.Queries == nil || d.Changes == nil || d.Identity == nil || d.Security == nil {
		return nil, errors.New("workspace capabilities required")
	}
	return &Module{c: c, d: d, loginAttempts: map[string]loginWindow{}}, nil
}
func (m *Module) Routes() []httpapi.Route {
	routes := []httpapi.Route{}
	for _, r := range []struct{ method, path, op string }{{"GET", "/space", "space.read"}, {"PATCH", "/space", "space.edit"}, {"GET", "/projects", "projects.list"}, {"POST", "/projects", "project.create"}, {"PATCH", "/projects/{id}", "project.edit"}, {"GET", "/people", "people.list"}, {"POST", "/tasks/{id}/project", "task.project"}, {"POST", "/account/password", "identity.password.change"}, {"POST", "/users/{id}/password", "identity.password"}, {"GET", "/me", "session.viewer"}, {"GET", "/members", "members.list"}, {"POST", "/identities", "identity.create"}, {"POST", "/credentials", "identity.issue"}, {"POST", "/credentials/revoke", "identity.revoke"}, {"GET", "/app", "ui.app"}, {"POST", "/session", "session.login"}, {"DELETE", "/session", "session.logout"}, {"GET", "/tasks", "tasks.list"}, {"POST", "/tasks", "tasks.create"}, {"GET", "/tasks/{id}", "tasks.get"}, {"PATCH", "/tasks/{id}", "tasks.edit"}, {"POST", "/tasks/{id}/commits", "commits.append"},
		{"POST", "/tasks/{id}/participants", "participants.set"}, {"GET", "/changes", "changes.read"}, {"POST", "/members", "members.set"}, {"DELETE", "/members", "members.remove"}} {
		routes = append(routes, httpapi.Route{Method: r.method, Path: r.path, Operation: r.op, Handler: http.HandlerFunc(m.serve)})
	}
	return routes
}

// setSession writes the session cookie. It is Secure except on a loopback
// host, where traffic never leaves the machine and plain HTTP is allowed.
func (m *Module) setSession(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: httpx.SessionCookie, Value: value, Path: "/", HttpOnly: true, Secure: !httpx.LoopbackHost(r.Host), SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}

// current answers a task command with the caller's current task detail. The
// receipt keeps only the command's Ref, so a replay also shows current facts.
func (m *Module) current(ctx context.Context, token string) func(api.Ref, error) (any, error) {
	return func(ref api.Ref, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return m.d.Queries.Get(ctx, token, ref.TaskID, 0)
	}
}
func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	token := httpx.Credential(r)
	path := r.URL.Path
	if strings.HasSuffix(path, "/app") {
		b, _ := assets.ReadFile("app.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
		return
	}
	if strings.HasSuffix(path, "/session") {
		m.session(w, r)
		return
	}
	ctx := r.Context()
	key := r.Header.Get("Idempotency-Key")
	taskID := r.PathValue("id")
	var result any
	var err error
	switch {
	case strings.HasSuffix(path, "/space"):
		if r.Method == "GET" {
			result, err = m.d.Catalog.Space(ctx, token)
		} else {
			var in catalog.SpaceEdit
			if !httpx.Decode(w, r, &in) {
				return
			}
			if err = m.d.Catalog.EditSpace(ctx, token, key, in); err == nil {
				result, err = m.d.Catalog.Space(ctx, token)
			}
		}
	case strings.HasSuffix(path, "/people"):
		result, err = m.d.Queries.People(ctx, token)
	case strings.HasSuffix(path, "/project"):
		var in api.ProjectMove
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		err = m.d.Commands.SetProject(ctx, token, key, in)
		result = map[string]bool{"ok": err == nil}
	case strings.Contains(path, "/projects"):
		if r.Method == "GET" {
			result, err = m.d.Catalog.Projects(ctx, token)
		} else {
			var in catalog.ProjectEdit
			if !httpx.Decode(w, r, &in) {
				return
			}
			in.ID = taskID
			var id string
			if id, err = m.d.Catalog.SaveProject(ctx, token, key, in); err == nil {
				result, err = m.d.Catalog.Project(ctx, token, id)
			}
		}
	case strings.HasSuffix(path, "/me"):
		result, err = m.d.Queries.Viewer(ctx, token)
	case strings.HasSuffix(path, "/members") && r.Method == "GET":
		result, err = m.d.Queries.Members(ctx, token)
	case strings.HasSuffix(path, "/password"), strings.HasSuffix(path, "/identities"), strings.HasSuffix(path, "/credentials"), strings.HasSuffix(path, "/credentials/revoke"):
		result, err = m.administration(w, r, token)
		if err == errDecoded {
			return
		}
	case strings.HasSuffix(path, "/changes"):
		result, err = m.d.Changes.Changes(ctx, token, r.URL.Query().Get("cursor"))
	case strings.HasSuffix(path, "/members"):
		var in authorization.Member
		if !httpx.Decode(w, r, &in) {
			return
		}
		if r.Method == "DELETE" {
			in.Active = false
		}
		err = m.d.Security.SetMember(ctx, token, key, in)
		result = map[string]bool{"ok": err == nil}
	case strings.HasSuffix(path, "/participants"):
		var in api.Participation
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		err = m.d.Commands.SetParticipant(ctx, token, key, in)
		result = map[string]bool{"ok": err == nil}
	case strings.HasSuffix(path, "/commits"):
		var in api.Comment
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Commands.Comment(ctx, token, key, in)
	case taskID != "" && r.Method == "GET":
		after := int64(0)
		if raw := r.URL.Query().Get("after"); raw != "" {
			after, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || after < 0 {
				httpx.Fail(w, fault.ErrInput)
				return
			}
		}
		result, err = m.d.Queries.Get(ctx, token, taskID, after)
	case taskID != "" && r.Method == "PATCH":
		var in api.Edit
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.current(ctx, token)(m.d.Commands.Edit(ctx, token, key, in))
	case r.Method == "GET":
		result, err = m.d.Queries.List(ctx, token)
	case r.Method == "POST":
		var in api.Create
		if !httpx.Decode(w, r, &in) {
			return
		}
		result, err = m.current(ctx, token)(m.d.Commands.Create(ctx, token, key, in))
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, result)
}
