package workspace

import (
	"errors"
	"net/http"
	"strings"
	"time"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
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
	if strings.HasSuffix(r.URL.Path, "/account/password") {
		var in struct{ CurrentPassword, Password string }
		if !httpx.Decode(w, r, &in) {
			return nil, errDecoded
		}
		e = m.d.Security.ChangePassword(ctx, token, key, security.ChangePasswordInput{PasswordInput: security.PasswordInput{Tenant: p.Tenant, Subject: p.ID, Password: in.Password}, CurrentPassword: in.CurrentPassword})
		return map[string]bool{"ok": e == nil}, e
	}
	view, e := m.d.Queries.Viewer(ctx, token)
	if e != nil {
		return nil, e
	}
	if !view.CanAdmin {
		return nil, az.ErrForbidden
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/password"):
		var in struct{ Password string }
		if !httpx.Decode(w, r, &in) {
			return nil, errDecoded
		}
		e = m.d.Security.SetPassword(ctx, token, key, security.PasswordInput{Tenant: p.Tenant, Subject: r.PathValue("id"), Password: in.Password})
		return map[string]bool{"ok": e == nil}, e
	case strings.HasSuffix(r.URL.Path, "/identities"):
		var in struct{ Subject, Kind, OwnerID, Password string }
		if !httpx.Decode(w, r, &in) {
			return nil, errDecoded
		}
		if in.Kind == "human" {
			if in.OwnerID != "" {
				return nil, fault.Input(fault.CodeInvalid, "OwnerID") // people have no owner
			}
			e = m.d.Security.CreateUser(ctx, token, key, security.UserInput{Tenant: p.Tenant, Username: in.Subject, Password: in.Password})
		} else {
			if in.Password != "" {
				return nil, fault.Input(fault.CodeInvalid, "Password") // Agents sign in with credentials
			}
			e = m.d.Security.Create(ctx, token, key, identity.Principal{Tenant: p.Tenant, ID: in.Subject, Kind: in.Kind, OwnerID: in.OwnerID})
		}
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
			Subject   string
			ExpiresAt time.Time
		}
		if !httpx.Decode(w, r, &in) {
			return nil, errDecoded
		}
		return m.d.Security.Issue(ctx, token, key, security.IssueInput{Tenant: p.Tenant, Subject: in.Subject, ExpiresAt: in.ExpiresAt})
	}
}
