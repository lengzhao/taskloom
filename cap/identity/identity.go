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
	Authenticate(ctx context.Context, secret string) (Principal, error)
}

// CredentialLookup is the authentication consumer's sole storage capability.
type CredentialLookup interface {
	FindCredential(ctx context.Context, digest string) (Credential, error)
}

// Principals is the cohesive directory and registration capability used by administration.
type PrincipalReader interface {
	Resolve(ctx context.Context, tenant, subject string) (Principal, error)
}
type Principals interface {
	PrincipalReader
	CreatePrincipal(ctx context.Context, value Principal) error
}
type Credentials interface {
	SaveCredential(ctx context.Context, value Credential) error
	RevokeCredential(ctx context.Context, tenant, credentialID string) error
	RevokeSubjectCredentials(ctx context.Context, tenant, subject string) error
}

// BootstrapClaim belongs exclusively to the offline administration entry point.
type BootstrapClaim interface {
	ClaimBootstrap(ctx context.Context) error
}
