// Package identity defines trusted local identity without ORM dependencies.
package identity

import (
	"context"
	"errors"
	"time"
)

var ErrUnauthenticated = errors.New("invalid or expired credential")

type Principal struct {
	Tenant  string
	ID      string
	Kind    string // human or agent
	OwnerID string // attribution only, never inherited permissions
	// CredentialID is set by authentication: the credential this request used.
	// It is the only verifiable identity of a running Agent instance.
	CredentialID string
}
type Credential struct {
	ID        string
	Principal Principal
	Digest    string
	ExpiresAt time.Time
	Revoked   bool
	// Session marks a credential created by password login. Changing a
	// password revokes sessions only; issued credentials stay valid.
	Session bool
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
	RevokeSubjectSessions(ctx context.Context, tenant, subject string) error
}

// BootstrapClaim belongs exclusively to the offline administration entry point.
type BootstrapClaim interface {
	ClaimBootstrap(ctx context.Context) error
}

// Passwords stores only salted password hashes, keyed by tenant and human subject.
type Passwords interface {
	PasswordHash(ctx context.Context, tenant, subject string) (string, error)
	SetPasswordHash(ctx context.Context, tenant, subject, hash string) error
	// SingleLoginTenant is used only when deployment has not configured loginTenant.
	// Ambiguous multi-tenant deployments must configure an explicit login scope.
	SingleLoginTenant(ctx context.Context) (string, error)
}
