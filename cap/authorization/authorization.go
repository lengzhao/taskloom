// Package authorization defines explicit action grants, never owner inheritance.
package authorization

import (
	"context"
	"errors"

	"github.com/lengzhao/taskloom/cap/identity"
)

var ErrForbidden = errors.New("action not permitted")

type Scope struct {
	Kind string
	ID   string
}
type Grant struct {
	Tenant, Subject, Action string
	Scope                   Scope
}
type Authorizer interface {
	Check(context.Context, identity.Principal, string, Scope) error
}
type Repository interface {
	HasGrant(context.Context, Grant) (bool, error)
	PutGrant(context.Context, Grant) error
	RemoveGrant(context.Context, Grant) error
}
