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
type MemberStore interface {
	MemberReader
	SetMember(ctx context.Context, value Member) error
}

// Relations is the read-only set consumed by business access checks.
type Relations interface {
	MemberReader
	IsAssigned(ctx context.Context, tenant, taskID, subject string) (bool, error)
	// AssignedBy is who made the task's current assignment; "" when unknown
	// (no assignment, or one recorded before assigners were kept).
	AssignedBy(ctx context.Context, tenant, taskID string) (string, error)
	HasParticipant(ctx context.Context, value Participant) (bool, error)
}

// ParticipantLister lists a task's current participants, the people a
// question may be addressed to besides the owner.
type ParticipantLister interface {
	ListParticipants(ctx context.Context, tenant, taskID string) ([]string, error)
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

// MemberQuery narrows and pages the active member directory, which is ordered
// by subject. Search matches part of the subject; Kind is human, agent or empty
// for both; After is the last subject of the previous page; Limit 0 lists all.
// MemberQuery filters active members. Subject matches one member exactly;
// Search matches any part of the subject.
type MemberQuery struct {
	Subject, Search, Kind, After string
	Limit                        int
}

// MemberDirectory is the read-only capability needed by task member selectors.
type MemberDirectory interface {
	MemberReader
	ListActiveMembers(ctx context.Context, tenant string, q MemberQuery) ([]MemberOption, error)
	ListMembers(ctx context.Context, tenant string) ([]DirectoryMember, error)
}

// DirectoryMember is administrative metadata; inactive members remain visible here.
type DirectoryMember struct {
	Subject, Kind, OwnerID, Role string
	Active                       bool
}
