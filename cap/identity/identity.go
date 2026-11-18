// Package identity defines trusted local identity without ORM dependencies.
package identity

import (
	"context"
	"errors"
	"time"
)

var ErrUnauthenticated = errors.New("invalid or expired credential")

type Principal struct {
	Tenant            string
	ID                string
	Kind              string // human or agent
	OwnerID           string // attribution only, never inherited permissions
	InstanceID        string
	Incarnation       string
	CredentialPurpose string // business or runtime; filled by authentication only
}
type Credential struct {
	ID        string
	Principal Principal
	Digest    string
	ExpiresAt time.Time
	Revoked   bool
}
type Authenticator interface {
	Authenticate(context.Context, string) (Principal, error)
}
type Directory interface {
	Resolve(context.Context, string, string) (Principal, error)
}

// Repository writes join the caller's transaction; no plaintext secret is stored.
type Repository interface {
	Directory
	CreatePrincipal(context.Context, Principal) error
	SaveCredential(context.Context, Credential) error
	FindCredential(context.Context, string) (Credential, error)
	RevokeCredential(context.Context, string, string) error
	RevokeSubjectCredentials(context.Context, string, string) error
	ClaimBootstrap(context.Context) error
}
