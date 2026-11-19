package security

import (
	"context"
	"github.com/lengzhao/taskloom/cap/identity"
	"time"

	"github.com/lengzhao/taskloom/cap/authorization"
)

type MemberCommands interface {
	SetMember(ctx context.Context, credential, idempotencyKey string, value authorization.Member) error
}

// Administration exposes ordinary authenticated administration, never bootstrap/recovery.
type Administration interface {
	MemberCommands
	Create(ctx context.Context, credential, idempotencyKey string, value identity.Principal) error
	Issue(ctx context.Context, credential, idempotencyKey string, value IssueInput) (Issued, error)
	Revoke(ctx context.Context, credential, idempotencyKey string, value RevokeInput) error
}
type Issued struct{ ID, Secret string }
type IssueInput struct {
	Tenant, Subject                  string
	InstanceID, Incarnation, Purpose string
	ExpiresAt                        time.Time
}
type RevokeInput struct{ Tenant, CredentialID string }
