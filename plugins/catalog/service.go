package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	api "github.com/lengzhao/taskloom/cap/catalog"
	cmd "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/runtime/command"
	"sort"
	"strings"
	"unicode/utf8"
)

// Deps has six fields: projects and spaces are tenant-wide metadata whose
// commands read the caller and membership, write the store inside the shared
// command lifecycle, and announce the change; none of these is a second role.
type Deps struct {
	Transactions unitofwork.Viewer      `json:"transactions"`
	Identity     identity.Authenticator `json:"identity"`
	Members      az.MemberDirectory     `json:"members"`
	Store        api.Store              `json:"store"`
	Commands     cmd.Handler            `json:"commands"`
	// Events announces space and project changes; viewers refresh access from it.
	Events record.EventWriter `json:"events"`
}

// Config holds the deployment's recommended tags: only a source for an
// administrator's explicit import, never written on its own.
type Config struct {
	RecommendedTags []api.TagSpec `json:"recommendedTags"`
}

func (c *Config) Validate() error {
	if len(c.RecommendedTags) > api.MaxTags {
		return errors.New("recommendedTags: too many tags")
	}
	seen := map[string]bool{}
	for i, t := range c.RecommendedTags {
		name := strings.TrimSpace(t.Name)
		if name == "" || utf8.RuneCountInString(name) > api.MaxTagName || utf8.RuneCountInString(t.Description) > api.MaxTagDetails {
			return fmt.Errorf("recommendedTags[%d]: name must be 1-%d characters", i, api.MaxTagName)
		}
		if seen[strings.ToLower(name)] {
			return fmt.Errorf("recommendedTags: duplicate name %q", name)
		}
		seen[strings.ToLower(name)] = true
		c.RecommendedTags[i].Name = name
	}
	return nil
}

type Service struct {
	c Config
	d Deps
}

func init() {
	pluginkit.Register("catalog/projects", New)
}

func New(c Config, d Deps) (*Service, error) {
	if d.Transactions == nil || d.Identity == nil || d.Members == nil || d.Store == nil || d.Commands == nil || d.Events == nil {
		return nil, errors.New("catalog capabilities required")
	}
	if e := c.Validate(); e != nil {
		return nil, e
	}
	return &Service{c: c, d: d}, nil
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
	e := s.d.Transactions.View(ctx, func(tx context.Context) error {
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
	e := s.d.Transactions.View(ctx, func(tx context.Context) error {
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
	e := s.d.Transactions.View(ctx, func(tx context.Context) error {
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
			return nil, fault.Input("invalid_name_or_detail", "Name")
		}
		old, e := s.d.Store.GetSpace(tx, p.Tenant)
		if e != nil {
			return nil, e
		}
		if old.Version != in.ExpectedVersion {
			return nil, fault.Conflict(fault.CodeVersionChanged)
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
			return nil, fault.Input("invalid_name_or_detail", "Name")
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
			return nil, fault.Conflict(fault.CodeVersionChanged)
		}
		owner := in.Owner
		if owner == "" {
			owner = p.ID
		}
		members, e := s.d.Members.ListActiveMembers(tx, p.Tenant, az.MemberQuery{})
		if e != nil {
			return nil, e
		}
		kinds := map[string]string{}
		for _, m := range members {
			kinds[m.Subject] = m.Kind
		}
		if kinds[owner] != "human" {
			return nil, fault.Input("owner_not_person", "Owner")
		}
		// Members may be people or Agents; the owner is already included.
		if len(in.Members) > api.MaxMembers {
			return nil, fault.Input("too_many_members", "Members")
		}
		// A member who has since left the tenant drops out on the next save,
		// so a client resending the list it read is not refused; naming
		// someone new who is not an active member still is.
		listed := map[string]bool{}
		for _, m := range old.Members {
			listed[m] = true
		}
		included := []string{}
		seen := map[string]bool{owner: true}
		for _, m := range in.Members {
			if kinds[m] == "" {
				if listed[m] {
					continue
				}
				return nil, fault.Input("member_inactive", "Members")
			}
			if !seen[m] {
				seen[m] = true
				included = append(included, m)
			}
		}
		id := in.ID
		if id == "" {
			var b [16]byte
			if _, e = rand.Read(b[:]); e != nil {
				return nil, e
			}
			id = hex.EncodeToString(b[:])
		}
		project := api.Project{Tenant: p.Tenant, ID: id, Name: strings.TrimSpace(in.Name), Description: in.Description, Owner: owner, Members: included, Archived: in.Archived, Version: old.Version + 1}
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

// Tags lists the vocabulary by name, archived included, for any active member.
func (s *Service) Tags(ctx context.Context, token string) ([]api.Tag, error) {
	out := []api.Tag{}
	e := s.d.Transactions.View(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		if out, e = s.d.Store.ListTags(tx, p.Tenant); e != nil {
			return e
		}
		sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
		return nil
	})
	return out, e
}

func (s *Service) Tag(ctx context.Context, token, id string) (api.Tag, error) {
	var out api.Tag
	e := s.d.Transactions.View(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		out, e = s.d.Store.GetTag(tx, p.Tenant, id)
		return e
	})
	return out, e
}

// SaveTag creates or replaces a tag; only a human administrator may.
func (s *Service) SaveTag(ctx context.Context, token, key string, in api.TagEdit) (string, error) {
	p, e := s.actor(ctx, token)
	if e != nil {
		return "", e
	}
	op := "tag.edit"
	if in.ID == "" {
		op = "tag.create"
	}
	req := cmd.Request{Token: token, Key: key, Operation: op, Resource: p.Tenant + "/" + in.ID, Authorize: func(tx context.Context, p identity.Principal) error {
		m, e := s.d.Members.GetMember(tx, p.Tenant, p.ID)
		if e != nil || !m.Active || m.Role != "admin" || p.Kind != "human" {
			return az.ErrForbidden
		}
		return nil
	}}
	b, e := command.Run(ctx, s.d.Commands, req, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, fault.Input(fault.CodeRequired, "Name")
		}
		if utf8.RuneCountInString(name) > api.MaxTagName {
			return nil, fault.Input("too_long", "Name")
		}
		if utf8.RuneCountInString(in.Description) > api.MaxTagDetails {
			return nil, fault.Input("too_long", "Description")
		}
		tags, e := s.d.Store.ListTags(tx, p.Tenant)
		if e != nil {
			return nil, e
		}
		old := api.Tag{}
		for _, t := range tags {
			if t.ID == in.ID {
				old = t
			} else if strings.EqualFold(t.Name, name) {
				return nil, fault.Conflict("tag_name_taken")
			}
		}
		if in.ID != "" && old.ID == "" {
			return nil, fault.ErrNotFound
		}
		if old.Version != in.ExpectedVersion {
			return nil, fault.Conflict(fault.CodeVersionChanged)
		}
		id := in.ID
		if id == "" {
			if len(tags) >= api.MaxTags {
				return nil, fault.Conflict("too_many_tags")
			}
			var b [16]byte
			if _, e = rand.Read(b[:]); e != nil {
				return nil, e
			}
			id = hex.EncodeToString(b[:])
		}
		tag := api.Tag{Tenant: p.Tenant, ID: id, Name: name, Description: in.Description, Archived: in.Archived, Version: old.Version + 1}
		if e = s.d.Store.SaveTag(tx, tag); e != nil {
			return nil, e
		}
		// Every reader of the vocabulary refreshes it; no task changes.
		if e = s.d.Events.AppendEvent(tx, record.Event{Tenant: p.Tenant, Resource: p.Tenant, Kind: record.EventAccessChanged}); e != nil {
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

// missing is the recommended tags the vocabulary does not name yet,
// ignoring case; archived tags count as present. A renamed tag counts by its
// current name, so its original name is offered again for the administrator
// to decide on in the preview.
func (s *Service) missing(tags []api.Tag) []api.TagSpec {
	have := map[string]bool{}
	for _, t := range tags {
		have[strings.ToLower(t.Name)] = true
	}
	out := []api.TagSpec{}
	for _, r := range s.c.RecommendedTags {
		if !have[strings.ToLower(r.Name)] {
			out = append(out, r)
		}
	}
	return out
}

func (s *Service) RecommendedTags(ctx context.Context, token string) ([]api.TagSpec, error) {
	out := []api.TagSpec{}
	e := s.d.Transactions.View(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		tags, e := s.d.Store.ListTags(tx, p.Tenant)
		if e != nil {
			return e
		}
		out = s.missing(tags)
		return nil
	})
	return out, e
}

// ImportRecommendedTags adds the missing recommended tags in one command.
// Nothing missing writes nothing; exceeding the space's limit refuses all.
func (s *Service) ImportRecommendedTags(ctx context.Context, token, key string) ([]string, error) {
	p, e := s.actor(ctx, token)
	if e != nil {
		return nil, e
	}
	req := cmd.Request{Token: token, Key: key, Operation: "tag.import", Resource: p.Tenant, Authorize: func(tx context.Context, p identity.Principal) error {
		m, e := s.d.Members.GetMember(tx, p.Tenant, p.ID)
		if e != nil || !m.Active || m.Role != "admin" || p.Kind != "human" {
			return az.ErrForbidden
		}
		return nil
	}}
	b, e := command.Run(ctx, s.d.Commands, req, struct{}{}, func(tx context.Context, p identity.Principal) ([]byte, error) {
		tags, e := s.d.Store.ListTags(tx, p.Tenant)
		if e != nil {
			return nil, e
		}
		add := s.missing(tags)
		ids := []string{}
		if len(add) == 0 {
			return json.Marshal(ids)
		}
		if len(tags)+len(add) > api.MaxTags {
			return nil, fault.Conflict("too_many_tags")
		}
		for _, r := range add {
			var raw [16]byte
			if _, e = rand.Read(raw[:]); e != nil {
				return nil, e
			}
			id := hex.EncodeToString(raw[:])
			if e = s.d.Store.SaveTag(tx, api.Tag{Tenant: p.Tenant, ID: id, Name: r.Name, Description: r.Description, Version: 1}); e != nil {
				return nil, e
			}
			ids = append(ids, id)
		}
		if e = s.d.Events.AppendEvent(tx, record.Event{Tenant: p.Tenant, Resource: p.Tenant, Kind: record.EventAccessChanged}); e != nil {
			return nil, e
		}
		return json.Marshal(ids)
	})
	var ids []string
	if e == nil {
		e = json.Unmarshal(b, &ids)
	}
	return ids, e
}
