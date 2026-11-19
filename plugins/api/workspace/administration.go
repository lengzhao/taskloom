package workspace

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/security"
	"github.com/lengzhao/taskloom/runtime/httpx"
)

// Decode already writes the protocol error. Keep it separate from domain failures.
var errDecoded = errors.New("response already written")

// Tenant comes from authentication; browser DTOs cannot choose a tenant or role.
func (m *Module) administration(w http.ResponseWriter, r *http.Request, token string) (any, error) {
	ctx := r.Context()
	p, e := m.d.Identity.Authenticate(ctx, token)
	if e != nil {
		return nil, e
	}
	key := r.Header.Get("Idempotency-Key")
	switch {
	case strings.HasSuffix(r.URL.Path, "/identities"):
		var in struct{ Subject, Kind, OwnerID string }
		if !httpx.Decode(w, r, &in) {
			return nil, errDecoded
		}
		e = m.d.Security.Create(ctx, token, key, identity.Principal{Tenant: p.Tenant, ID: in.Subject, Kind: in.Kind, OwnerID: in.OwnerID})
		return map[string]bool{"ok": e == nil}, e
	case strings.HasSuffix(r.URL.Path, "/credentials/revoke"):
		var in struct{ CredentialID string }
		if !httpx.Decode(w, r, &in) {
			return nil, errDecoded
		}
		e = m.d.Security.Revoke(ctx, token, key, security.RevokeInput{Tenant: p.Tenant, CredentialID: in.CredentialID})
		return map[string]bool{"ok": e == nil}, e
	default:
		var in struct {
			Subject, InstanceID, Incarnation, Purpose string
			ExpiresAt                                 time.Time
		}
		if !httpx.Decode(w, r, &in) {
			return nil, errDecoded
		}
		return m.d.Security.Issue(ctx, token, key, security.IssueInput{Tenant: p.Tenant, Subject: in.Subject, InstanceID: in.InstanceID, Incarnation: in.Incarnation, Purpose: in.Purpose, ExpiresAt: in.ExpiresAt})
	}
}
