// Package security implements local administrative commands. It has no HTTP
// route; bootstrap is only exposed by the explicitly invoked operations CLI.
package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lengzhao/pluginkit"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"strings"
	"time"

	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/runtime/command"
)

// Bootstrap needs five independent capabilities; normal administrative commands
// additionally use the shared command lifecycle. No hidden service container.
type Deps struct {
	Passwords    identity.Passwords        `json:"passwords"`
	Commands     capcommand.Handler        `json:"commands"`
	Transactions unitofwork.Manager        `json:"transactions"`
	Principals   identity.Principals       `json:"principals"`
	Credentials  identity.Credentials      `json:"credentials"`
	Bootstrap    identity.BootstrapClaim   `json:"bootstrap"`
	Members      authorization.MemberStore `json:"members"`
	Audit        record.AuditWriter        `json:"audit"`
	Events       record.EventWriter        `json:"events"`
}
type Service struct{ d Deps }

func init() {
	pluginkit.Register("commands/security", func(_ struct{}, d Deps) (*Service, error) { return New(d), nil })
}

func New(d Deps) *Service { return &Service{d} }

// AdminInput is an offline operator's request to create or recover a tenant
// administrator. People authenticate only with a password, so no credential
// is issued here: the administrator logs in to get one.
type AdminInput struct {
	Tenant, Subject, Operator, Reason string
	Password                          string
}

func valid(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\n\r*")
}
func credential(p identity.Principal, expires time.Time) (identity.Credential, capsecurity.Issued, error) {
	if !expires.After(time.Now()) || expires.After(time.Now().Add(90*24*time.Hour)) {
		return identity.Credential{}, capsecurity.Issued{}, fault.Input("invalid_expiry", "ExpiresAt")
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return identity.Credential{}, capsecurity.Issued{}, errors.New("credential entropy unavailable")
	}
	secret := hex.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(secret))
	digest := hex.EncodeToString(sum[:])
	id := digest[:32]
	return identity.Credential{ID: id, Principal: p, Digest: digest, ExpiresAt: expires.UTC()}, capsecurity.Issued{ID: id, Secret: secret}, nil
}
func (s *Service) Bootstrap(ctx context.Context, in AdminInput) error {
	if !valid(in.Tenant) || !valid(in.Subject) || !valid(in.Operator) || !valid(in.Reason) {
		return errors.New("bootstrap requires tenant, subject, operator and reason")
	}
	p := identity.Principal{Tenant: in.Tenant, ID: in.Subject, Kind: "human"}
	hash, err := passwordHash(in.Password)
	if err != nil {
		return err
	}
	return s.d.Transactions.Do(ctx, func(tx context.Context) error {
		if err := s.d.Bootstrap.ClaimBootstrap(tx); err != nil {
			return err
		}
		if err := s.d.Principals.CreatePrincipal(tx, p); err != nil {
			return err
		}
		if err := s.d.Passwords.SetPasswordHash(tx, p.Tenant, p.ID, hash); err != nil {
			return err
		}
		if err := s.d.Members.SetMember(tx, authorization.Member{Tenant: p.Tenant, Subject: p.ID, Role: "admin", Active: true}); err != nil {
			return err
		}
		return s.d.Audit.AppendAudit(tx, record.Audit{Tenant: p.Tenant, Subject: in.Operator, Operation: "identity.bootstrap", Resource: p.ID, Outcome: "committed", Reason: in.Reason})
	})
}
func request(token, key, op, tenant string) capcommand.Request {
	return capcommand.Request{Token: token, Key: key, Operation: op, Resource: tenant}
}
func (s *Service) Create(ctx context.Context, token, key string, p identity.Principal) error {
	_, err := command.Run(ctx, s.d.Commands, request(token, key, "identity.create", p.Tenant), p, func(tx context.Context, actor identity.Principal) ([]byte, error) {
		if p.Tenant != actor.Tenant {
			return nil, authorization.ErrForbidden
		}
		// Humans are created with a password (CreateUser); this creates Agents,
		// which authenticate only with issued credentials.
		if !valid(p.ID) || p.Kind != "agent" || p.CredentialID != "" {
			return nil, fault.Input("invalid_subject", "Subject")
		}
		if !valid(p.OwnerID) {
			return nil, fault.Input(fault.CodeRequired, "OwnerID")
		}
		owner, err := s.d.Principals.Resolve(tx, p.Tenant, p.OwnerID)
		if err != nil || owner.Kind != "human" {
			return nil, authorization.ErrForbidden
		}
		member, err := s.d.Members.GetMember(tx, p.Tenant, owner.ID)
		if err != nil || !member.Active {
			return nil, authorization.ErrForbidden
		}
		if e := s.d.Principals.CreatePrincipal(tx, p); e != nil {
			return nil, e
		}
		return nil, s.d.Members.SetMember(tx, authorization.Member{Tenant: p.Tenant, Subject: p.ID, Role: "member", Active: true})
	})
	return err
}
func (s *Service) Issue(ctx context.Context, token, key string, in capsecurity.IssueInput) (capsecurity.Issued, error) {
	// The secret stays only in this call. Persist the ID receipt, never the secret.
	var issued capsecurity.Issued
	created := false
	result, err := command.Run(ctx, s.d.Commands, request(token, key, "identity.issue", in.Tenant), in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if p.Tenant != in.Tenant {
			return nil, authorization.ErrForbidden
		}
		subject, err := s.d.Principals.Resolve(tx, in.Tenant, in.Subject)
		if err != nil {
			return nil, err
		}
		// Every member may hold issued credentials; people can also sign in with a password.
		member, err := s.d.Members.GetMember(tx, in.Tenant, in.Subject)
		if err != nil || !member.Active {
			return nil, authorization.ErrForbidden
		}
		c, out, err := credential(subject, in.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if err = s.d.Credentials.SaveCredential(tx, c); err != nil {
			return nil, err
		}
		issued = out
		created = true
		return json.Marshal(out.ID)
	})
	if err != nil {
		return capsecurity.Issued{}, err
	}
	if !created {
		if err = json.Unmarshal(result, &issued.ID); err != nil {
			return capsecurity.Issued{}, fault.ErrUnavailable
		}
	}
	return issued, nil
}
func (s *Service) Revoke(ctx context.Context, token, key string, in capsecurity.RevokeInput) error {
	_, err := command.Run(ctx, s.d.Commands, request(token, key, "identity.revoke", in.Tenant), in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if p.Tenant != in.Tenant {
			return nil, authorization.ErrForbidden
		}
		return nil, s.d.Credentials.RevokeCredential(tx, in.Tenant, in.CredentialID)
	})
	return err
}

// Logout revokes exactly the caller's own credential. The idempotency key is
// the credential ID, so each session ends once and a later login is not
// mistaken for a replay.
func (s *Service) Logout(ctx context.Context, token string) error {
	sum := sha256.Sum256([]byte(token))
	id := hex.EncodeToString(sum[:])[:32]
	req := capcommand.Request{Token: token, Key: id, Operation: "session.logout", Resource: id, Authorize: func(context.Context, identity.Principal) error { return nil }}
	_, err := command.Run(ctx, s.d.Commands, req, struct{}{}, func(tx context.Context, p identity.Principal) ([]byte, error) {
		return nil, s.d.Credentials.RevokeCredential(tx, p.Tenant, p.CredentialID)
	})
	return err
}

// SetMember changes a concrete tenant relationship. Revocation applies to all
// task reads/writes/replays without deleting historic authorship or ownership.
func (s *Service) SetMember(ctx context.Context, token, key string, in authorization.Member) error {
	_, err := command.Run(ctx, s.d.Commands, request(token, key, "member.set", in.Tenant), in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if p.Tenant != in.Tenant || (in.Role != "admin" && in.Role != "member") {
			return nil, authorization.ErrForbidden
		}
		if p.ID == in.Subject && (!in.Active || in.Role != "admin") {
			return nil, authorization.ErrForbidden
		}
		subject, e := s.d.Principals.Resolve(tx, in.Tenant, in.Subject)
		if e != nil {
			return nil, e
		}
		if in.Role == "admin" && subject.Kind != "human" {
			return nil, authorization.ErrForbidden
		}
		if e = s.d.Members.SetMember(tx, in); e != nil {
			return nil, e
		}
		return nil, s.d.Events.AppendEvent(tx, record.Event{Tenant: in.Tenant, Resource: in.Subject, Kind: record.EventAccessChanged})
	})
	return err
}

// Recover is an offline operator capability, never callable by an ordinary API.
// Every old credential of this human is revoked atomically with replacing the
// password and restoring this human’s tenant administrator membership.
func (s *Service) Recover(ctx context.Context, in AdminInput) error {
	if !valid(in.Tenant) || !valid(in.Subject) || !valid(in.Operator) || !valid(in.Reason) {
		return errors.New("recovery requires tenant, subject, operator and reason")
	}
	hash, err := passwordHash(in.Password)
	if err != nil {
		return err
	}
	return s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, err := s.d.Principals.Resolve(tx, in.Tenant, in.Subject)
		if err != nil {
			return err
		}
		if p.Kind != "human" {
			return authorization.ErrForbidden
		}
		if err = s.d.Credentials.RevokeSubjectCredentials(tx, p.Tenant, p.ID); err != nil {
			return err
		}
		if err = s.d.Passwords.SetPasswordHash(tx, p.Tenant, p.ID, hash); err != nil {
			return err
		}
		if err := s.d.Members.SetMember(tx, authorization.Member{Tenant: p.Tenant, Subject: p.ID, Role: "admin", Active: true}); err != nil {
			return err
		}
		return s.d.Audit.AppendAudit(tx, record.Audit{Tenant: p.Tenant, Subject: in.Operator, Operation: "identity.recover", Resource: p.ID, Outcome: "committed", Reason: in.Reason})
	})
}

// EnrollAgent is an explicitly invoked offline operator capability. Each
// credential it issues identifies one running instance of the Agent.
type AgentEnrollment struct {
	Tenant, Subject, Owner, Operator, Reason string
	ExpiresAt                                time.Time
}

func (s *Service) EnrollAgent(ctx context.Context, in AgentEnrollment) (capsecurity.Issued, error) {
	if !valid(in.Tenant) || !valid(in.Subject) || !valid(in.Owner) || !valid(in.Operator) || !valid(in.Reason) {
		return capsecurity.Issued{}, fault.ErrInput
	}
	var issued capsecurity.Issued
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		owner, e := s.d.Principals.Resolve(tx, in.Tenant, in.Owner)
		if e != nil {
			return e
		}
		if owner.Kind != "human" {
			return authorization.ErrForbidden
		}
		p, e := s.d.Principals.Resolve(tx, in.Tenant, in.Subject)
		if errors.Is(e, fault.ErrNotFound) {
			p = identity.Principal{Tenant: in.Tenant, ID: in.Subject, Kind: "agent", OwnerID: in.Owner}
			if e = s.d.Principals.CreatePrincipal(tx, p); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		if p.Kind != "agent" || p.OwnerID != in.Owner {
			return authorization.ErrForbidden
		}
		if e = s.d.Members.SetMember(tx, authorization.Member{Tenant: p.Tenant, Subject: p.ID, Role: "member", Active: true}); e != nil {
			return e
		}
		c, out, e := credential(p, in.ExpiresAt)
		if e != nil {
			return e
		}
		if e = s.d.Credentials.SaveCredential(tx, c); e != nil {
			return e
		}
		if e = s.d.Audit.AppendAudit(tx, record.Audit{Tenant: p.Tenant, Subject: in.Operator, Operation: "identity.enroll-agent", Resource: p.ID, Outcome: "committed", Reason: in.Reason, OwnerID: p.OwnerID, CredentialID: c.ID}); e != nil {
			return e
		}
		issued = out
		return nil
	})
	if e != nil {
		return capsecurity.Issued{}, e
	}
	return issued, nil
}
