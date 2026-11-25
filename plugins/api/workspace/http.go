package workspace

import (
	"embed"
	"errors"
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

// Tasks have their own adapters (api/task, api/workflow); this module serves
// the session, the space and its projects, the member directory, the change
// feed and administration.
type Deps struct {
	Catalog  catalog.Service         `json:"catalog"`
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
	if d.Catalog == nil || d.Queries == nil || d.Changes == nil || d.Identity == nil || d.Security == nil {
		return nil, errors.New("workspace capabilities required")
	}
	return &Module{c: c, d: d, loginAttempts: map[string]loginWindow{}}, nil
}
func (m *Module) Routes() []httpapi.Route {
	routes := []httpapi.Route{}
	for _, r := range []struct{ method, path, op string }{
		{"GET", "/app", "ui.app"}, {"POST", "/session", "session.login"}, {"DELETE", "/session", "session.logout"},
		{"GET", "/me", "session.viewer"}, {"POST", "/account/password", "identity.password.change"},
		{"GET", "/space", "space.read"}, {"PATCH", "/space", "space.edit"},
		{"GET", "/projects", "projects.list"}, {"POST", "/projects", "project.create"}, {"PATCH", "/projects/{id}", "project.edit"},
		{"GET", "/people", "people.list"}, {"GET", "/changes", "changes.read"},
		{"GET", "/admin/members", "members.list"}, {"POST", "/admin/members", "members.set"},
		{"POST", "/admin/identities", "identity.create"}, {"POST", "/admin/users/{id}/password", "identity.password"},
		{"POST", "/admin/credentials", "identity.issue"}, {"POST", "/admin/credentials/revoke", "identity.revoke"},
	} {
		routes = append(routes, httpapi.Route{Method: r.method, Path: r.path, Operation: r.op, Handler: http.HandlerFunc(m.serve)})
	}
	return routes
}

// setSession writes the session cookie. It is Secure except on a loopback
// host, where traffic never leaves the machine and plain HTTP is allowed.
func (m *Module) setSession(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: httpx.SessionCookie, Value: value, Path: "/", HttpOnly: true, Secure: !httpx.LoopbackHost(r.Host), SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
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
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		result, err = m.d.Queries.People(ctx, token, authorization.MemberQuery{Search: q.Get("q"), Kind: q.Get("kind"), After: q.Get("after"), Limit: limit})
	case strings.Contains(path, "/projects"):
		if r.Method == "GET" {
			result, err = m.d.Catalog.Projects(ctx, token)
		} else {
			var in catalog.ProjectEdit
			if !httpx.Decode(w, r, &in) {
				return
			}
			in.ID = r.PathValue("id")
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
		// The tenant comes from authentication, never from the request.
		var in struct {
			Subject, Role string
			Active        bool
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		var viewer api.Viewer
		if viewer, err = m.d.Queries.Viewer(ctx, token); err == nil {
			err = m.d.Security.SetMember(ctx, token, key, authorization.Member{Tenant: viewer.Tenant, Subject: in.Subject, Role: in.Role, Active: in.Active})
		}
		result = map[string]bool{"ok": err == nil}
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, result)
}
