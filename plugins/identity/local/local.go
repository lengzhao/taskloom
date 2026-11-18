// Package local authenticates opaque credentials against current local facts.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/identity"
)

type Deps struct {
	Store identity.CredentialLookup `json:"store"`
}
type Service struct{ store identity.CredentialLookup }

func init() {
	pluginkit.Register("identity/local", func(_ struct{}, d Deps) (*Service, error) { return New(d) })
}
func New(d Deps) (*Service, error) {
	if d.Store == nil {
		return nil, errors.New("identity store required")
	}
	return &Service{store: d.Store}, nil
}
func (s *Service) Authenticate(ctx context.Context, secret string) (identity.Principal, error) {
	if len(secret) < 32 || len(secret) > 256 {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	h := sha256.Sum256([]byte(secret))
	c, err := s.store.FindCredential(ctx, hex.EncodeToString(h[:]))
	if err != nil || c.Revoked || !c.ExpiresAt.After(time.Now()) {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	return c.Principal, nil
}
