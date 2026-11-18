package local_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/lengzhao/taskloom/cap/identity"
	local "github.com/lengzhao/taskloom/plugins/identity/local"
	"testing"
	"time"
)

// Authentication can be built using a provider with no registration, revocation
// or bootstrap methods at all.
type credentialLookup struct{ value identity.Credential }

func (l credentialLookup) FindCredential(_ context.Context, digest string) (identity.Credential, error) {
	if digest != l.value.Digest {
		return identity.Credential{}, identity.ErrUnauthenticated
	}
	return l.value, nil
}
func TestAuthenticationNeedsOnlyCredentialLookup(t *testing.T) {
	secret := "test-only-secret-with-at-least-32-bytes"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	service, e := local.New(local.Deps{Store: credentialLookup{identity.Credential{Principal: identity.Principal{Tenant: "t", ID: "u", Kind: "human"}, Digest: digest, ExpiresAt: time.Now().Add(time.Hour)}}})
	if e != nil {
		t.Fatal(e)
	}
	p, e := service.Authenticate(context.Background(), secret)
	if e != nil || p.ID != "u" {
		t.Fatal(p, e)
	}
}
