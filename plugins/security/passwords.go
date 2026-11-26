package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"time"

	az "github.com/lengzhao/taskloom/cap/authorization"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	api "github.com/lengzhao/taskloom/cap/security"
	domain "github.com/lengzhao/taskloom/domain/security"
	"github.com/lengzhao/taskloom/runtime/command"
)

// passwordHash hashes a password given offline, with a random salt.
func passwordHash(password string) (string, error) {
	if !domain.ValidPassword(password) {
		return "", fault.Input("weak_password", "Password")
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", fault.ErrUnavailable
	}
	return domain.DerivePassword(password, salt[:]), nil
}

// The command receipt fingerprint must not expose a cheap SHA256 password oracle.
// Bind a costly KDF proof to the request; store a separately random-salted hash.
func passwordMaterial(tenant, subject, key, password string) (hash, proof string, err error) {
	if !domain.ValidPassword(password) {
		return "", "", fault.Input("weak_password", "Password")
	}
	if !valid(subject) {
		return "", "", fault.Input("invalid_subject", "Subject")
	}
	if key == "" || len(key) > 128 {
		return "", "", fault.Input(fault.CodeIdempotencyKeyRequired, "Idempotency-Key")
	}
	var salt [16]byte
	if _, err = rand.Read(salt[:]); err != nil {
		return "", "", fault.ErrUnavailable
	}
	hash = domain.DerivePassword(password, salt[:])
	sum := sha256.Sum256([]byte(tenant + "\x00" + subject + "\x00" + key))
	proof = domain.DerivePassword(password, sum[:16])
	return
}
func (s *Service) CreateUser(ctx context.Context, token, key string, in api.UserInput) error {
	hash, proof, e := passwordMaterial(in.Tenant, in.Username, key, in.Password)
	if e != nil {
		return e
	}
	input := struct{ Tenant, Username, Proof string }{in.Tenant, in.Username, proof}
	_, e = command.Run(ctx, s.d.Commands, request(token, key, "identity.create", in.Tenant), input, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if in.Tenant != p.Tenant {
			return nil, az.ErrForbidden
		}
		if e := s.d.Principals.CreatePrincipal(tx, identity.Principal{Tenant: p.Tenant, ID: in.Username, Kind: "human"}); e != nil {
			return nil, e
		}
		if e := s.d.Members.SetMember(tx, az.Member{Tenant: p.Tenant, Subject: in.Username, Role: "member", Active: true}); e != nil {
			return nil, e
		}
		return nil, s.d.Passwords.SetPasswordHash(tx, p.Tenant, in.Username, hash)
	})
	return e
}
func (s *Service) SetPassword(ctx context.Context, token, key string, in api.PasswordInput) error {
	hash, proof, e := passwordMaterial(in.Tenant, in.Subject, key, in.Password)
	if e != nil {
		return e
	}
	input := struct{ Tenant, Subject, Proof string }{in.Tenant, in.Subject, proof}
	_, e = command.Run(ctx, s.d.Commands, request(token, key, "identity.password", in.Tenant), input, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if in.Tenant != p.Tenant {
			return nil, az.ErrForbidden
		}
		target, e := s.d.Principals.Resolve(tx, p.Tenant, in.Subject)
		if e != nil {
			return nil, e
		}
		if target.Kind != "human" {
			return nil, fault.Input("not_a_person", "Subject")
		}
		return nil, s.savePassword(tx, p.Tenant, in.Subject, hash)
	})
	return e
}

// savePassword replaces the password and ends every password-login session;
// issued credentials stay valid. Offline recovery is the single entry point
// that revokes every credential of a person.
func (s *Service) savePassword(ctx context.Context, tenant, subject, hash string) error {
	if e := s.d.Passwords.SetPasswordHash(ctx, tenant, subject, hash); e != nil {
		return e
	}
	return s.d.Credentials.RevokeSubjectSessions(ctx, tenant, subject)
}
func (s *Service) ChangePassword(ctx context.Context, token, key string, in api.ChangePasswordInput) error {
	old, e := s.d.Passwords.PasswordHash(ctx, in.Tenant, in.Subject)
	if e != nil && !errors.Is(e, fault.ErrNotFound) {
		return e
	}
	if len(in.CurrentPassword) > 128 {
		return fault.Input("too_long", "CurrentPassword")
	}
	// Every person has a password, so the current one is always required.
	if !domain.PasswordMatches(in.CurrentPassword, old) {
		return az.ErrForbidden
	}
	hash, proof, e := passwordMaterial(in.Tenant, in.Subject, key, in.Password)
	if e != nil {
		return e
	}
	req := capcommand.Request{Token: token, Key: key, Operation: "identity.password.change", Resource: in.Subject,
		Authorize: func(tx context.Context, p identity.Principal) error {
			if p.Tenant != in.Tenant || p.ID != in.Subject || p.Kind != "human" {
				return az.ErrForbidden
			}
			m, e := s.d.Members.GetMember(tx, p.Tenant, p.ID)
			if e != nil || !m.Active {
				return az.ErrForbidden
			}
			return nil
		}}
	input := struct{ Tenant, Subject, Proof string }{in.Tenant, in.Subject, proof}
	_, e = command.Run(ctx, s.d.Commands, req, input, func(tx context.Context, p identity.Principal) ([]byte, error) {
		current, e := s.d.Passwords.PasswordHash(tx, p.Tenant, p.ID)
		if e != nil && !errors.Is(e, fault.ErrNotFound) {
			return nil, e
		}
		if current != old {
			return nil, fault.ErrConflict
		}
		return nil, s.savePassword(tx, p.Tenant, p.ID, hash)
	})
	return e
}
func (s *Service) Login(ctx context.Context, in api.LoginInput) (api.Issued, error) {
	if !valid(in.Username) || !domain.ValidPassword(in.Password) {
		return api.Issued{}, identity.ErrUnauthenticated
	}
	if in.Tenant == "" {
		var e error
		in.Tenant, e = s.d.Passwords.SingleLoginTenant(ctx)
		if e != nil {
			return api.Issued{}, identity.ErrUnauthenticated
		}
	}
	hash, e := s.d.Passwords.PasswordHash(ctx, in.Tenant, in.Username)
	if e != nil || hash == "" {
		// Equal KDF work for absent accounts; no username existence signal.
		domain.DerivePassword(in.Password, make([]byte, 16))
		return api.Issued{}, identity.ErrUnauthenticated
	}
	if !domain.PasswordMatches(in.Password, hash) {
		return api.Issued{}, identity.ErrUnauthenticated
	}
	var issued api.Issued
	e = s.d.Transactions.Do(ctx, func(tx context.Context) error {
		current, e := s.d.Passwords.PasswordHash(tx, in.Tenant, in.Username)
		if e != nil || current != hash {
			return identity.ErrUnauthenticated
		}
		p, e := s.d.Principals.Resolve(tx, in.Tenant, in.Username)
		if e != nil || p.Kind != "human" {
			return identity.ErrUnauthenticated
		}
		m, e := s.d.Members.GetMember(tx, p.Tenant, p.ID)
		if e != nil || !m.Active || (m.Role != "admin" && m.Role != "member") {
			return identity.ErrUnauthenticated
		}
		c, v, e := credential(p, time.Now().Add(time.Hour))
		if e != nil {
			return e
		}
		c.Session = true
		if e = s.d.Credentials.SaveCredential(tx, c); e != nil {
			return e
		}
		if e = s.d.Audit.AppendAudit(tx, record.Audit{Tenant: p.Tenant, Subject: p.ID, Operation: "identity.login", Resource: p.ID, Outcome: "committed"}); e != nil {
			return e
		}
		issued = v
		return nil
	})
	return issued, e
}
