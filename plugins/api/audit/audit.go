// Package audit serves the tenant audit trail to administrators. It is the
// reader that makes the audit records written by every command verifiable.
package audit

import (
	"context"
	"net/http"
	"strconv"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/runtime/httpx"
)

type Deps struct {
	Transactions  unitofwork.Manager     `json:"transactions"`
	Identity      identity.Authenticator `json:"identity"`
	Authorization az.Authorizer          `json:"authorization"`
	Audit         record.AuditReader     `json:"audit"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("api/audit", func(_ struct{}, d Deps) (*Module, error) {
		if d.Transactions == nil || d.Identity == nil || d.Authorization == nil || d.Audit == nil {
			return nil, fault.ErrInput
		}
		return &Module{d}, nil
	})
}

const maxEntries = 500

func (m *Module) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "GET", Path: "/admin/audit", Operation: "audit.read", Handler: http.HandlerFunc(m.serve)}}
}

// List returns the newest audit entries of the caller's tenant; only active
// human administrators may read them.
func (m *Module) List(ctx context.Context, token string, limit int) ([]record.Audit, error) {
	if limit <= 0 || limit > maxEntries {
		limit = 100
	}
	var out []record.Audit
	e := m.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := m.d.Identity.Authenticate(tx, token)
		if e != nil {
			return e
		}
		if e = m.d.Authorization.Check(tx, p, "audit.read", p.Tenant); e != nil {
			return e
		}
		out, e = m.d.Audit.ListAudit(tx, p.Tenant, limit)
		return e
	})
	return out, e
}

func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	token := httpx.Credential(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := m.List(r.Context(), token, limit)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, out)
}
