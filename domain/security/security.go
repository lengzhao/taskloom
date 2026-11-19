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

func New(d Deps) *Service { return &Service{d} }

type BootstrapInput struct {
	Tenant, Subject, Operator, Reason string
	ExpiresAt                         time.Time
}

func valid(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\n\r*")
}
func credential(p identity.Principal, expires time.Time) (identity.Credential, capsecurity.Issued, error) {
	if p.CredentialPurpose == "" {
		p.CredentialPurpose = "business"
	}
	if !expires.After(time.Now()) || expires.After(time.Now().Add(90*24*time.Hour)) {
		return identity.Credential{}, capsecurity.Issued{}, fault.ErrInput
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
func (s *Service) Bootstrap(ctx context.Context, in BootstrapInput) (capsecurity.Issued, error) {
	if !valid(in.Tenant) || !valid(in.Subject) || !valid(in.Operator) || !valid(in.Reason) {
		return capsecurity.Issued{}, errors.New("bootstrap requires tenant, subject, operator and reason")
	}
	p := identity.Principal{Tenant: in.Tenant, ID: in.Subject, Kind: "human"}
	c, issued, err := credential(p, in.ExpiresAt)
	if err != nil {
		return capsecurity.Issued{}, err
	}
	err = s.d.Transactions.Do(ctx, func(tx context.Context) error {
		if err := s.d.Bootstrap.ClaimBootstrap(tx); err != nil {
			return err
		}
		if err := s.d.Principals.CreatePrincipal(tx, p); err != nil {
			return err
		}
		if err := s.d.Credentials.SaveCredential(tx, c); err != nil {
			return err
		}
		if err := s.d.Members.SetMember(tx, authorization.Member{Tenant: p.Tenant, Subject: p.ID, Role: "admin", Active: true}); err != nil {
			return err
		}
		if err := s.d.Audit.AppendAudit(tx, record.Audit{Tenant: p.Tenant, Subject: in.Operator, Operation: "identity.bootstrap", Resource: p.ID, Outcome: "committed", Reason: in.Reason}); err != nil {
			return err
		}
		return s.d.Events.AppendEvent(tx, record.Event{Tenant: p.Tenant, Resource: p.ID, Kind: "identity.bootstrap"})
	})
	if err != nil {
		return capsecurity.Issued{}, err
	}
	return issued, nil
}
func request(token, key, op, tenant string) capcommand.Request {
	return capcommand.Request{Token: token, Key: key, Operation: op, Resource: tenant}
}
func (s *Service) Create(ctx context.Context, token, key string, p identity.Principal) error {
	_, err := command.Run(ctx, s.d.Commands, request(token, key, "identity.create", p.Tenant), p, func(tx context.Context, actor identity.Principal) ([]byte, error) {
		if p.Tenant != actor.Tenant {
			return nil, authorization.ErrForbidden
		}
		if !valid(p.ID) || (p.Kind != "human" && p.Kind != "agent") {
			return nil, fault.ErrInput
		}
		if p.Kind == "human" && (p.OwnerID != "" || p.InstanceID != "" || p.Incarnation != "") {
			return nil, fault.ErrInput
		}
		if p.Kind == "agent" {
			if !valid(p.OwnerID) || p.InstanceID != "" || p.Incarnation != "" || p.CredentialPurpose != "" {
				return nil, fault.ErrInput
			}
			owner, err := s.d.Principals.Resolve(tx, p.Tenant, p.OwnerID)
			if err != nil || owner.Kind != "human" {
				return nil, authorization.ErrForbidden
			}
			member, err := s.d.Members.GetMember(tx, p.Tenant, owner.ID)
			if err != nil || !member.Active {
				return nil, authorization.ErrForbidden
			}
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
		member, err := s.d.Members.GetMember(tx, in.Tenant, in.Subject)
		if err != nil || !member.Active {
			return nil, authorization.ErrForbidden
		}
		if subject.Kind == "agent" {
			if !valid(in.InstanceID) || !valid(in.Incarnation) || (in.Purpose != "business" && in.Purpose != "runtime") {
				return nil, fault.ErrInput
			}
		} else if in.InstanceID != "" || in.Incarnation != "" || (in.Purpose != "" && in.Purpose != "business") {
			return nil, fault.ErrInput
		}
		subject.InstanceID = in.InstanceID
		subject.Incarnation = in.Incarnation
		subject.CredentialPurpose = in.Purpose
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
		return nil, s.d.Members.SetMember(tx, in)
	})
	return err
}

// Recover is an offline operator capability, never callable by an ordinary API.
// Every old credential of this human is revoked atomically with issuing the new
// credential and restoring this human’s tenant administrator membership.
func (s *Service) Recover(ctx context.Context, in BootstrapInput) (capsecurity.Issued, error) {
	if !valid(in.Tenant) || !valid(in.Subject) || !valid(in.Operator) || !valid(in.Reason) {
		return capsecurity.Issued{}, errors.New("recovery requires tenant, subject, operator and reason")
	}
	var issued capsecurity.Issued
	err := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, err := s.d.Principals.Resolve(tx, in.Tenant, in.Subject)
		if err != nil {
			return err
		}
		if p.Kind != "human" {
			return authorization.ErrForbidden
		}
		c, out, err := credential(p, in.ExpiresAt)
		if err != nil {
			return err
		}
		if err = s.d.Credentials.RevokeSubjectCredentials(tx, p.Tenant, p.ID); err != nil {
			return err
		}
		if err = s.d.Credentials.SaveCredential(tx, c); err != nil {
			return err
		}
		if err := s.d.Members.SetMember(tx, authorization.Member{Tenant: p.Tenant, Subject: p.ID, Role: "admin", Active: true}); err != nil {
			return err
		}
		if err = s.d.Audit.AppendAudit(tx, record.Audit{Tenant: p.Tenant, Subject: in.Operator, Operation: "identity.recover", Resource: p.ID, Outcome: "committed", Reason: in.Reason}); err != nil {
			return err
		}
		if err = s.d.Events.AppendEvent(tx, record.Event{Tenant: p.Tenant, Resource: p.ID, Kind: "identity.recover"}); err != nil {
			return err
		}
		issued = out
		return nil
	})
	if err != nil {
		return capsecurity.Issued{}, err
	}
	return issued, nil
}

// EnrollAgent is an explicitly invoked offline operator capability. It binds a
// service credential to one runtime incarnation and never exposes a model/tool
// credential as runtime stop-proof authority.
type AgentEnrollment struct {
	BootstrapInput
	Owner, InstanceID, Incarnation, Purpose string
}

func (s *Service) EnrollAgent(ctx context.Context, in AgentEnrollment) (capsecurity.Issued, error) {
	if !valid(in.Tenant) || !valid(in.Subject) || !valid(in.Owner) || !valid(in.Operator) || !valid(in.Reason) || !valid(in.InstanceID) || !valid(in.Incarnation) || (in.Purpose != "business" && in.Purpose != "runtime") {
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
		p.InstanceID = in.InstanceID
		p.Incarnation = in.Incarnation
		p.CredentialPurpose = in.Purpose
		c, out, e := credential(p, in.ExpiresAt)
		if e != nil {
			return e
		}
		if e = s.d.Credentials.SaveCredential(tx, c); e != nil {
			return e
		}
		if e = s.d.Audit.AppendAudit(tx, record.Audit{Tenant: p.Tenant, Subject: in.Operator, Operation: "identity.enroll-agent", Resource: p.ID, Outcome: "committed", Reason: in.Reason, OwnerID: p.OwnerID, InstanceID: p.InstanceID, Incarnation: p.Incarnation}); e != nil {
			return e
		}
		if e = s.d.Events.AppendEvent(tx, record.Event{Tenant: p.Tenant, Resource: p.ID, Kind: "identity.enrolled"}); e != nil {
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
