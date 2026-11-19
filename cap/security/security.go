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
	CreateUser(ctx context.Context, credential, key string, value UserInput) error
	SetPassword(ctx context.Context, credential, key string, value PasswordInput) error
	ChangePassword(ctx context.Context, credential, key string, value ChangePasswordInput) error
	Login(ctx context.Context, value LoginInput) (Issued, error)
	// Logout revokes the credential it is called with, ending that session only.
	Logout(ctx context.Context, credential string) error
	MemberCommands
	Create(ctx context.Context, credential, idempotencyKey string, value identity.Principal) error
	Issue(ctx context.Context, credential, idempotencyKey string, value IssueInput) (Issued, error)
	Revoke(ctx context.Context, credential, idempotencyKey string, value RevokeInput) error
}
type Issued struct{ ID, Secret string }
type IssueInput struct {
	Tenant, Subject string
	ExpiresAt       time.Time
}
type RevokeInput struct{ Tenant, CredentialID string }

type UserInput struct{ Tenant, Username, Password string }
type PasswordInput struct{ Tenant, Subject, Password string }
type ChangePasswordInput struct {
	PasswordInput
	CurrentPassword string
}
type LoginInput struct{ Tenant, Username, Password string }
