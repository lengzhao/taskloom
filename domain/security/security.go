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
	"strings"
	"time"

	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/runtime/command"
)

// Bootstrap needs five independent capabilities; normal administrative commands
// additionally use the shared command lifecycle. No hidden service container.
type Deps struct {
	Commands     *command.Runner          `json:"commands"`
	Transactions unitofwork.Manager       `json:"transactions"`
	Identities   identity.Repository      `json:"identities"`
	Grants       authorization.Repository `json:"grants"`
	Audit        record.Audits            `json:"audit"`
	Events       record.Events            `json:"events"`
}
type Service struct{ d Deps }

func New(d Deps) *Service { return &Service{d} }

type Issued struct{ ID, Secret string }
type BootstrapInput struct {
	Tenant, Subject, Operator, Reason string
	ExpiresAt                         time.Time
}
type IssueInput struct {
	Tenant, Subject                  string
	InstanceID, Incarnation, Purpose string
	ExpiresAt                        time.Time
}
type RevokeInput struct{ Tenant, CredentialID string }

func valid(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\n\r*")
}
func credential(p identity.Principal, expires time.Time) (identity.Credential, Issued, error) {
	if p.CredentialPurpose == "" {
		p.CredentialPurpose = "business"
	}
	if !expires.After(time.Now()) || expires.After(time.Now().Add(90*24*time.Hour)) {
		return identity.Credential{}, Issued{}, errors.New("credential expiry must be within 90 days")
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return identity.Credential{}, Issued{}, errors.New("credential entropy unavailable")
	}
	secret := hex.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(secret))
	digest := hex.EncodeToString(sum[:])
	id := digest[:32]
	return identity.Credential{ID: id, Principal: p, Digest: digest, ExpiresAt: expires.UTC()}, Issued{ID: id, Secret: secret}, nil
}
func (s *Service) Bootstrap(ctx context.Context, in BootstrapInput) (Issued, error) {
	if !valid(in.Tenant) || !valid(in.Subject) || !valid(in.Operator) || !valid(in.Reason) {
		return Issued{}, errors.New("bootstrap requires tenant, subject, operator and reason")
	}
	p := identity.Principal{Tenant: in.Tenant, ID: in.Subject, Kind: "human"}
	c, issued, err := credential(p, in.ExpiresAt)
	if err != nil {
		return Issued{}, err
	}
	err = s.d.Transactions.Do(ctx, func(tx context.Context) error {
		if err := s.d.Identities.ClaimBootstrap(tx); err != nil {
			return err
		}
		if err := s.d.Identities.CreatePrincipal(tx, p); err != nil {
			return err
		}
		if err := s.d.Identities.SaveCredential(tx, c); err != nil {
			return err
		}
		for _, action := range []string{"identity.create", "identity.issue", "identity.revoke", "access.grant", "access.revoke"} {
			if err := s.d.Grants.PutGrant(tx, authorization.Grant{Tenant: p.Tenant, Subject: p.ID, Action: action, Scope: authorization.Scope{Kind: "tenant", ID: p.Tenant}}); err != nil {
				return err
			}
		}
		if err := s.d.Audit.AppendAudit(tx, record.Audit{Tenant: p.Tenant, Subject: in.Operator, Operation: "identity.bootstrap", Resource: p.ID, Outcome: "committed", Reason: in.Reason}); err != nil {
			return err
		}
		return s.d.Events.AppendEvent(tx, record.Event{Tenant: p.Tenant, Resource: p.ID, Kind: "identity.bootstrap"})
	})
	if err != nil {
		return Issued{}, err
	}
	return issued, nil
}
func request(token, key, op, tenant string) command.Request {
	return command.Request{Token: token, Key: key, Operation: op, Scope: authorization.Scope{Kind: "tenant", ID: tenant}}
}
func (s *Service) Create(ctx context.Context, token, key string, p identity.Principal) error {
	_, err := command.Run(ctx, s.d.Commands, request(token, key, "identity.create", p.Tenant), p, func(tx context.Context, actor identity.Principal) ([]byte, error) {
		if p.Tenant != actor.Tenant {
			return nil, authorization.ErrForbidden
		}
		if !valid(p.ID) || (p.Kind != "human" && p.Kind != "agent") {
			return nil, errors.New("invalid principal")
		}
		if p.Kind == "human" && (p.OwnerID != "" || p.InstanceID != "" || p.Incarnation != "") {
			return nil, errors.New("human cannot carry runtime binding")
		}
		if p.Kind == "agent" {
			if !valid(p.OwnerID) || p.InstanceID != "" || p.Incarnation != "" || p.CredentialPurpose != "" {
				return nil, errors.New("agent requires owner; runtime binding belongs to credentials")
			}
			owner, err := s.d.Identities.Resolve(tx, p.Tenant, p.OwnerID)
			if err != nil || owner.Kind != "human" {
				return nil, authorization.ErrForbidden
			}
		}
		return nil, s.d.Identities.CreatePrincipal(tx, p)
	})
	return err
}
func (s *Service) Issue(ctx context.Context, token, key string, in IssueInput) (Issued, error) {
	// The secret stays only in this call. Persist the ID receipt, never the secret.
	var issued Issued
	created := false
	result, err := command.Run(ctx, s.d.Commands, request(token, key, "identity.issue", in.Tenant), in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if p.Tenant != in.Tenant {
			return nil, authorization.ErrForbidden
		}
		subject, err := s.d.Identities.Resolve(tx, in.Tenant, in.Subject)
		if err != nil {
			return nil, err
		}
		if subject.Kind == "agent" {
			if !valid(in.InstanceID) || !valid(in.Incarnation) || (in.Purpose != "business" && in.Purpose != "runtime") {
				return nil, errors.New("agent credential requires instance, incarnation and purpose")
			}
		} else if in.InstanceID != "" || in.Incarnation != "" || (in.Purpose != "" && in.Purpose != "business") {
			return nil, errors.New("invalid human credential binding")
		}
		subject.InstanceID = in.InstanceID
		subject.Incarnation = in.Incarnation
		subject.CredentialPurpose = in.Purpose
		c, out, err := credential(subject, in.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if err = s.d.Identities.SaveCredential(tx, c); err != nil {
			return nil, err
		}
		issued = out
		created = true
		return json.Marshal(out.ID)
	})
	if err != nil {
		return Issued{}, err
	}
	if !created {
		if err = json.Unmarshal(result, &issued.ID); err != nil {
			return Issued{}, unitofwork.ErrUnavailable
		}
	}
	return issued, nil
}
func (s *Service) Revoke(ctx context.Context, token, key string, in RevokeInput) error {
	_, err := command.Run(ctx, s.d.Commands, request(token, key, "identity.revoke", in.Tenant), in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if p.Tenant != in.Tenant {
			return nil, authorization.ErrForbidden
		}
		return nil, s.d.Identities.RevokeCredential(tx, in.Tenant, in.CredentialID)
	})
	return err
}
func (s *Service) Grant(ctx context.Context, token, key string, g authorization.Grant) error {
	return s.changeGrant(ctx, token, key, g, false)
}
func (s *Service) Ungrant(ctx context.Context, token, key string, g authorization.Grant) error {
	return s.changeGrant(ctx, token, key, g, true)
}
func (s *Service) changeGrant(ctx context.Context, token, key string, g authorization.Grant, remove bool) error {
	op := "access.grant"
	if remove {
		op = "access.revoke"
	}
	_, err := command.Run(ctx, s.d.Commands, request(token, key, op, g.Tenant), g, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if p.Tenant != g.Tenant {
			return nil, authorization.ErrForbidden
		}
		if !valid(g.Subject) || !valid(g.Action) || !valid(g.Scope.ID) {
			return nil, authorization.ErrForbidden
		}
		switch g.Scope.Kind {
		case "tenant":
			if g.Scope.ID != g.Tenant {
				return nil, authorization.ErrForbidden
			}
		case "task", "project", "instance":
		default:
			return nil, authorization.ErrForbidden
		}
		if _, err := s.d.Identities.Resolve(tx, g.Tenant, g.Subject); err != nil {
			return nil, err
		}
		if remove {
			return nil, s.d.Grants.RemoveGrant(tx, g)
		}
		return nil, s.d.Grants.PutGrant(tx, g)
	})
	return err
}

// Recover is an offline operator capability, never callable by an ordinary API.
// Every old credential of this human is revoked atomically with issuing the new
// credential and restoring only the explicit administrative actions.
func (s *Service) Recover(ctx context.Context, in BootstrapInput) (Issued, error) {
	if !valid(in.Tenant) || !valid(in.Subject) || !valid(in.Operator) || !valid(in.Reason) {
		return Issued{}, errors.New("recovery requires tenant, subject, operator and reason")
	}
	var issued Issued
	err := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, err := s.d.Identities.Resolve(tx, in.Tenant, in.Subject)
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
		if err = s.d.Identities.RevokeSubjectCredentials(tx, p.Tenant, p.ID); err != nil {
			return err
		}
		if err = s.d.Identities.SaveCredential(tx, c); err != nil {
			return err
		}
		for _, action := range []string{"identity.create", "identity.issue", "identity.revoke", "access.grant", "access.revoke"} {
			if err = s.d.Grants.PutGrant(tx, authorization.Grant{Tenant: p.Tenant, Subject: p.ID, Action: action, Scope: authorization.Scope{Kind: "tenant", ID: p.Tenant}}); err != nil {
				return err
			}
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
		return Issued{}, err
	}
	return issued, nil
}
