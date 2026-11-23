// Package skills adapts skill publication, review, discovery, artifact reads
// and delivery decisions to HTTP under /skills. After that prefix the
// consumer paths match Skilla's (/capabilities, /releases/{ref}, ...).
package skills

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	api "github.com/lengzhao/taskloom/cap/skills"
	"github.com/lengzhao/taskloom/runtime/httpx"
)

type Config struct {
	// TransferTimeout replaces the server's read or write timeout for one
	// archive upload or download.
	TransferTimeout string `json:"transferTimeout"`
}

func (c *Config) SetDefaults() {
	if c.TransferTimeout == "" {
		c.TransferTimeout = "5m"
	}
}
func (c *Config) Validate() error {
	d, err := time.ParseDuration(c.TransferTimeout)
	if err != nil || d <= 0 {
		return errors.New("transferTimeout must be a positive duration")
	}
	return nil
}

type Deps struct {
	Commands api.Commands       `json:"commands"`
	Reviews  api.ReviewCommands `json:"reviews"`
	Queries  api.Queries        `json:"queries"`
}
type Module struct {
	timeout time.Duration
	d       Deps
}

func init() {
	pluginkit.Register("api/skills", func(c Config, d Deps) (*Module, error) {
		c.SetDefaults()
		if err := c.Validate(); err != nil {
			return nil, err
		}
		if d.Commands == nil || d.Reviews == nil || d.Queries == nil {
			return nil, fault.ErrInput
		}
		t, _ := time.ParseDuration(c.TransferTimeout)
		return &Module{t, d}, nil
	})
}

func (m *Module) Routes() []httpapi.Route {
	h := func(f http.HandlerFunc) http.Handler { return f }
	return []httpapi.Route{
		{Method: "GET", Path: "/skills/capabilities", Operation: "skill.search", Handler: h(m.search)},
		{Method: "GET", Path: "/skills/capabilities/resolve", Operation: "skill.resolve", Handler: h(m.resolve)},
		{Method: "POST", Path: "/skills/capabilities", Operation: "skill.publish", Handler: h(m.publish)},
		{Method: "POST", Path: "/skills/capabilities/publication-preflight", Operation: "skill.preflight", Handler: h(m.preflight)},
		{Method: "PATCH", Path: "/skills/capabilities/{id}", Operation: "skill.visibility", Handler: h(m.visibility)},
		{Method: "PUT", Path: "/skills/capabilities/{id}/collaborators", Operation: "skill.collaborators", Handler: h(m.collaborators)},
		{Method: "POST", Path: "/skills/capabilities/{id}/archive", Operation: "skill.archive", Handler: h(m.archive(true))},
		{Method: "POST", Path: "/skills/capabilities/{id}/restore", Operation: "skill.restore", Handler: h(m.archive(false))},
		{Method: "GET", Path: "/skills/releases/{ref}", Operation: "skill.release.read", Handler: h(m.release)},
		{Method: "GET", Path: "/skills/releases/{ref}/artifact", Operation: "skill.artifact", Handler: h(m.artifact)},
		{Method: "POST", Path: "/skills/capability-decisions", Operation: "skill.decide", Handler: h(m.decide)},
		{Method: "POST", Path: "/skills/admin/releases/{ref}/reviews", Operation: "skill.review", Handler: h(m.review)},
		{Method: "POST", Path: "/skills/admin/reindex", Operation: "skill.reindex", Handler: h(m.reindex)},
	}
}

func (m *Module) search(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	q := api.Query{Q: v.Get("q"), Match: v.Get("match")}
	var err error
	if q.Page, err = optInt(v.Get("page")); err == nil {
		q.Size, err = optInt(v.Get("size"))
	}
	if err == nil && v.Get("min_score") != "" {
		q.MinScore, err = strconv.ParseFloat(v.Get("min_score"), 64)
	}
	if err != nil {
		httpx.Fail(w, fault.ErrInput)
		return
	}
	page, err := m.d.Queries.Search(r.Context(), httpx.Credential(r), q)
	respond(w, page, err)
}

func (m *Module) resolve(w http.ResponseWriter, r *http.Request) {
	info, err := m.d.Queries.Resolve(r.Context(), httpx.Credential(r), r.URL.Query().Get("name"))
	respond(w, info, err)
}

// publish takes the raw zip archive as the body.
func (m *Module) publish(w http.ResponseWriter, r *http.Request) {
	m.extend(w)
	rel, err := m.d.Commands.Publish(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"), r.Body)
	respond(w, rel, err)
}

func (m *Module) preflight(w http.ResponseWriter, r *http.Request) {
	m.extend(w)
	rep, err := m.d.Commands.Preflight(r.Context(), httpx.Credential(r), r.Body)
	respond(w, rep, err)
}

func (m *Module) visibility(w http.ResponseWriter, r *http.Request) {
	var in struct{ Visibility string }
	if !httpx.Decode(w, r, &in) {
		return
	}
	c, err := m.d.Commands.SetVisibility(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"), r.PathValue("id"), in.Visibility)
	respond(w, c, err)
}

func (m *Module) collaborators(w http.ResponseWriter, r *http.Request) {
	var in struct{ Subjects []string }
	if !httpx.Decode(w, r, &in) {
		return
	}
	c, err := m.d.Commands.SetCollaborators(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"), r.PathValue("id"), in.Subjects)
	respond(w, c, err)
}

func (m *Module) archive(archived bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := m.d.Commands.SetArchived(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"), r.PathValue("id"), archived)
		respond(w, c, err)
	}
}

func (m *Module) release(w http.ResponseWriter, r *http.Request) {
	info, err := m.d.Queries.Release(r.Context(), httpx.Credential(r), r.PathValue("ref"))
	respond(w, info, err)
}

// artifact serves the exact published archive as an attachment; the host
// verifies X-Content-SHA256 before materializing it.
func (m *Module) artifact(w http.ResponseWriter, r *http.Request) {
	rel, body, err := m.d.Queries.Open(r.Context(), httpx.Credential(r), r.PathValue("ref"))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer body.Close()
	m.extend(w)
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Length", strconv.FormatInt(rel.Size, 10))
	h.Set("Content-Disposition", `attachment; filename="`+rel.Name+"-"+rel.Version+`.zip"`)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("X-Content-SHA256", rel.Digest)
	h.Set("ETag", `"`+rel.Digest+`"`)
	// Exactly Size bytes were published; a short copy must not look complete.
	if n, err := io.CopyN(w, body, rel.Size); err != nil || n != rel.Size {
		panic(http.ErrAbortHandler)
	}
}

func (m *Module) decide(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReleaseRef string
		Host       api.HostFacts
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	d, err := m.d.Queries.Decide(r.Context(), httpx.Credential(r), in.ReleaseRef, in.Host)
	respond(w, d, err)
}

func (m *Module) review(w http.ResponseWriter, r *http.Request) {
	var in struct{ Verdict, Reason string }
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := m.d.Reviews.Review(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"), r.PathValue("ref"), in.Verdict, in.Reason)
	respond(w, v, err)
}

func (m *Module) reindex(w http.ResponseWriter, r *http.Request) {
	n, err := m.d.Commands.Reindex(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"))
	respond(w, struct{ Indexed int }{n}, err)
}

func (m *Module) extend(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(m.timeout))
	_ = rc.SetWriteDeadline(time.Now().Add(m.timeout))
}

// respond writes v, or the error; a rejected publication carries its
// diagnostics so the author can fix every blocking finding at once.
func respond(w http.ResponseWriter, v any, err error) {
	if inv, ok := api.AsInvalid(err); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		httpx.Write(w, struct{ Diagnostics []api.Diagnostic }{inv.Diagnostics})
		return
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, v)
}

func optInt(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.Atoi(s)
}
