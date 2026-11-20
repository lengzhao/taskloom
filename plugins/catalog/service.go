package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	api "github.com/lengzhao/taskloom/cap/catalog"
	cmd "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/runtime/command"
	"strings"
)

// Deps has six fields: projects and spaces are tenant-wide metadata whose
// commands read the caller and membership, write the store inside the shared
// command lifecycle, and announce the change; none of these is a second role.
type Deps struct {
	Transactions unitofwork.Manager     `json:"transactions"`
	Identity     identity.Authenticator `json:"identity"`
	Members      az.MemberDirectory     `json:"members"`
	Store        api.Store              `json:"store"`
	Commands     cmd.Handler            `json:"commands"`
	// Events announces space and project changes; viewers refresh access from it.
	Events record.EventWriter `json:"events"`
}
type Service struct{ d Deps }

func init() {
	pluginkit.Register("catalog/projects", func(_ struct{}, d Deps) (*Service, error) {
		if d.Transactions == nil || d.Identity == nil || d.Members == nil || d.Store == nil || d.Commands == nil || d.Events == nil {
			return nil, errors.New("catalog capabilities required")
		}
		return &Service{d}, nil
	})
}
func (s *Service) actor(ctx context.Context, token string) (identity.Principal, error) {
	p, e := s.d.Identity.Authenticate(ctx, token)
	if e != nil {
		return p, e
	}
	m, e := s.d.Members.GetMember(ctx, p.Tenant, p.ID)
	if e != nil || !m.Active || (m.Role != "admin" && m.Role != "member") {
		return p, az.ErrForbidden
	}
	return p, nil
}
func (s *Service) Space(ctx context.Context, token string) (api.Space, error) {
	var out api.Space
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		out, e = s.d.Store.GetSpace(tx, p.Tenant)
		return e
	})
	return out, e
}
func (s *Service) Projects(ctx context.Context, token string) ([]api.Project, error) {
	out := []api.Project{}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		out, e = s.d.Store.ListProjects(tx, p.Tenant)
		return e
	})
	return out, e
}

// Project reads one project of the caller's tenant, visible like Projects.
func (s *Service) Project(ctx context.Context, token, id string) (api.Project, error) {
	var out api.Project
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		out, e = s.d.Store.GetProject(tx, p.Tenant, id)
		return e
	})
	return out, e
}
func valid(name, description string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 200 && len(description) <= 20000
}
func (s *Service) EditSpace(ctx context.Context, token, key string, in api.SpaceEdit) error {
	p, e := s.actor(ctx, token)
	if e != nil {
		return e
	}
	_, e = command.Run(ctx, s.d.Commands, cmd.Request{Token: token, Key: key, Operation: "space.edit", Resource: p.Tenant}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if !valid(in.Name, in.Description) {
			return nil, fault.ErrInput
		}
		old, e := s.d.Store.GetSpace(tx, p.Tenant)
		if e != nil {
			return nil, e
		}
		if old.Version != in.ExpectedVersion {
			return nil, fault.ErrConflict
		}
		space := api.Space{Tenant: p.Tenant, Name: strings.TrimSpace(in.Name), Description: in.Description, Version: old.Version + 1}
		if e = s.d.Store.SaveSpace(tx, space); e != nil {
			return nil, e
		}
		return nil, s.d.Events.AppendEvent(tx, record.Event{Tenant: p.Tenant, Resource: p.Tenant, Kind: record.EventAccessChanged})
	})
	return e
}
func (s *Service) SaveProject(ctx context.Context, token, key string, in api.ProjectEdit) (string, error) {
	p, e := s.actor(ctx, token)
	if e != nil {
		return "", e
	}
	op := "project.edit"
	if in.ID == "" {
		op = "project.create"
	}
	req := cmd.Request{Token: token, Key: key, Operation: op, Resource: p.Tenant + "/" + in.ID, Authorize: func(tx context.Context, p identity.Principal) error {
		if p.Kind != "human" {
			return az.ErrForbidden
		}
		m, e := s.d.Members.GetMember(tx, p.Tenant, p.ID)
		if e != nil || !m.Active {
			return az.ErrForbidden
		}
		if in.ID == "" {
			return nil
		}
		old, e := s.d.Store.GetProject(tx, p.Tenant, in.ID)
		if e != nil {
			return e
		}
		if old.Owner != p.ID && m.Role != "admin" {
			return az.ErrForbidden
		}
		return nil
	}}
	b, e := command.Run(ctx, s.d.Commands, req, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if !valid(in.Name, in.Description) {
			return nil, fault.ErrInput
		}
		old := api.Project{}
		if in.ID != "" {
			var e error
			old, e = s.d.Store.GetProject(tx, p.Tenant, in.ID)
			if e != nil {
				return nil, e
			}
		}
		if old.Version != in.ExpectedVersion {
			return nil, fault.ErrConflict
		}
		owner := in.Owner
		if owner == "" {
			owner = p.ID
		}
		members, e := s.d.Members.ListActiveMembers(tx, p.Tenant)
		if e != nil {
			return nil, e
		}
		found := false
		for _, m := range members {
			if m.Subject == owner && m.Kind == "human" {
				found = true
			}
		}
		if !found {
			return nil, fault.ErrInput
		}
		id := in.ID
		if id == "" {
			var b [16]byte
			if _, e = rand.Read(b[:]); e != nil {
				return nil, e
			}
			id = hex.EncodeToString(b[:])
		}
		project := api.Project{Tenant: p.Tenant, ID: id, Name: strings.TrimSpace(in.Name), Description: in.Description, Owner: owner, Archived: in.Archived, Version: old.Version + 1}
		if e = s.d.Store.SaveProject(tx, project); e != nil {
			return nil, e
		}
		if e = s.d.Events.AppendEvent(tx, record.Event{Tenant: p.Tenant, Resource: id, Kind: record.EventAccessChanged}); e != nil {
			return nil, e
		}
		return json.Marshal(id)
	})
	var id string
	if e == nil {
		e = json.Unmarshal(b, &id)
	}
	return id, e
}
