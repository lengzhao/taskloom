// Package authorization defines membership and task relationships.
package authorization

import (
	"context"
	"errors"

	"github.com/lengzhao/taskloom/cap/identity"
)

var ErrForbidden = errors.New("action not permitted")

type Member struct {
	Tenant, Subject, Role string
	Active                bool
}
type Participant struct{ Tenant, TaskID, Subject string }
type MemberReader interface {
	GetMember(ctx context.Context, tenant, subject string) (Member, error)
}
type MemberWriter interface {
	SetMember(ctx context.Context, value Member) error
}

// Relations is the read-only set consumed by business access checks.
type Relations interface {
	MemberReader
	HasParticipant(ctx context.Context, value Participant) (bool, error)
}
type ParticipantWriter interface {
	SetParticipant(ctx context.Context, value Participant, present bool) error
}

// Operation is a fixed named use case, not a configurable permission key.
// Resource is a task ID for task use cases, tenant ID for administration.
type Authorizer interface {
	Check(ctx context.Context, actor identity.Principal, operation, resourceID string) error
}

// MemberOption is public directory metadata, never credentials or runtime identity.
type MemberOption struct{ Subject, Kind, OwnerID string }

// MemberDirectory is the read-only capability needed by task member selectors.
type MemberDirectory interface {
	MemberReader
	ListActiveMembers(ctx context.Context, tenant string) ([]MemberOption, error)
}
