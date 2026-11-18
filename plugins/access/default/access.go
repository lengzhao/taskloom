// Package access checks explicit grants for configured actions and scopes.
package access

import (
	"context"
	"errors"
	"strings"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/identity"
)

func builtinActions() []string {
	return []string{"identity.create", "identity.issue", "identity.revoke", "access.grant", "access.revoke"}
}

type Config struct {
	Actions []string `json:"actions"`
}
type Deps struct {
	Store authorization.Repository `json:"store"`
}
type Service struct {
	store   authorization.Repository
	actions map[string]bool
}

func init() { pluginkit.Register("access/default", New) }
func New(c Config, d Deps) (*Service, error) {
	if d.Store == nil {
		return nil, errors.New("grant store required")
	}
	s := &Service{store: d.Store, actions: map[string]bool{}}
	for _, a := range append(append([]string{}, builtinActions()...), c.Actions...) {
		if a == "" || strings.Contains(a, "*") || s.actions[a] {
			return nil, errors.New("invalid or duplicate action")
		}
		s.actions[a] = true
	}
	return s, nil
}
func (s *Service) Check(ctx context.Context, p identity.Principal, action string, scope authorization.Scope) error {
	if p.Tenant == "" || p.ID == "" || !s.actions[action] || scope.ID == "" {
		return authorization.ErrForbidden
	}
	switch scope.Kind {
	case "tenant":
		if scope.ID != p.Tenant {
			return authorization.ErrForbidden
		}
	case "task", "project", "instance":
	default:
		return authorization.ErrForbidden
	}
	if p.CredentialPurpose == "runtime" && action != "execution.stopEvidence" {
		return authorization.ErrForbidden
	}
	if action == "execution.stopEvidence" && (p.Kind != "agent" || p.CredentialPurpose != "runtime") {
		return authorization.ErrForbidden
	}
	g := authorization.Grant{Tenant: p.Tenant, Subject: p.ID, Action: action, Scope: scope}
	ok, err := s.store.HasGrant(ctx, g)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	// An explicit tenant-scoped grant includes that tenant's resources. Resource
	// existence and tenant membership are still checked by each domain command.
	if scope.Kind != "tenant" {
		g.Scope = authorization.Scope{Kind: "tenant", ID: p.Tenant}
		ok, err = s.store.HasGrant(ctx, g)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return authorization.ErrForbidden
}
