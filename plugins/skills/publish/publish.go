// Package publish publishes and manages skill capabilities. An archive is
// inspected deterministically, its bytes are stored before the release is
// registered, and a failed or replayed registration deletes the new bytes.
// Discovery vectors are derived after commit and never gate publication.
package publish

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/blob"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/skills"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/domain/skillpkg"
	"github.com/lengzhao/taskloom/runtime/command"
)

type Config struct {
	// MaxBytes caps one uploaded archive.
	MaxBytes int64 `json:"maxBytes"`
	// EmbedTimeout bounds the post-commit embedding of one release.
	EmbedTimeout string `json:"embedTimeout"`
}

const (
	DefaultMaxBytes = 10 << 20
	maxCollaborator = 50
)

func (c *Config) SetDefaults() {
	if c.MaxBytes == 0 {
		c.MaxBytes = DefaultMaxBytes
	}
	if c.EmbedTimeout == "" {
		c.EmbedTimeout = "10s"
	}
}
func (c *Config) Validate() error {
	if c.MaxBytes < 1 {
		return errors.New("maxBytes must be positive")
	}
	if d, err := time.ParseDuration(c.EmbedTimeout); err != nil || d <= 0 {
		return errors.New("embedTimeout must be a positive duration")
	}
	return nil
}

// Deps has six fields, reviewed: publication runs the command lifecycle
// (commands) and registers the release (registry) after storing bytes
// (blobs); before accepting bytes it authenticates (identity) and resolves
// the capability (registry) in a read transaction (transactions); after
// commit it derives the discovery vector (embedder, optional). This mirrors
// commands/file; splitting would duplicate the same pre-check.
type Deps struct {
	Commands     capcommand.Handler     `json:"commands"`
	Transactions unitofwork.Manager     `json:"transactions"`
	Identity     identity.Authenticator `json:"identity"`
	Registry     skills.Registry        `json:"registry"`
	Blobs        blob.Store             `json:"blobs"`
	Embedder     skills.Embedder        `json:"embedder,omitempty"`
}

type Module struct {
	c     Config
	d     Deps
	embed time.Duration
	lim   skillpkg.Limits
}

func init() { pluginkit.Register("commands/skills", New) }

func New(c Config, d Deps) (*Module, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if d.Commands == nil || d.Transactions == nil || d.Identity == nil || d.Registry == nil || d.Blobs == nil {
		return nil, errors.New("skill publication capabilities required")
	}
	t, _ := time.ParseDuration(c.EmbedTimeout)
	return &Module{c: c, d: d, embed: t, lim: skillpkg.DefaultLimits}, nil
}

// pinned is what a publication's idempotency key fixes: name, version and bytes.
type pinned struct{ Name, Version, Digest string }

func (m *Module) Preflight(ctx context.Context, token string, body io.Reader) (skills.Report, error) {
	caller, err := m.authenticate(ctx, token)
	if err != nil {
		return skills.Report{}, err
	}
	raw, err := m.read(body)
	if err != nil {
		return skills.Report{}, err
	}
	man, diags := skillpkg.Inspect(raw, m.lim)
	r := skills.Report{Name: man.Name, Version: man.Version, Digest: digest(raw), Size: int64(len(raw)), Diagnostics: diags}
	if !blocking(diags) {
		cur, e := m.current(ctx, caller.Tenant, man.Name)
		if e != nil && !errors.Is(e, fault.ErrNotFound) {
			return skills.Report{}, e
		}
		if e == nil && !skillpkg.Newer(man.Version, cur.Version) {
			r.Diagnostics = append(r.Diagnostics, notNewer(cur.Version))
		}
	}
	r.Publishable = !blocking(r.Diagnostics)
	return r, nil
}

func (m *Module) Publish(ctx context.Context, token, key string, body io.Reader) (skills.Release, error) {
	if key == "" || len(key) > 128 {
		return skills.Release{}, fault.ErrInput
	}
	caller, err := m.authenticate(ctx, token)
	if err != nil {
		return skills.Release{}, err
	}
	raw, err := m.read(body)
	if err != nil {
		return skills.Release{}, err
	}
	man, diags := skillpkg.Inspect(raw, m.lim)
	if blocking(diags) {
		return skills.Release{}, &skills.Invalid{Diagnostics: diags}
	}
	// An existing capability is published to by its contributors; a new name
	// by any member. The command rechecks both before writing or replaying.
	op, resource := "skill.create", caller.Tenant
	if c, e := m.d.Registry.FindCapability(ctx, caller.Tenant, man.Name); e == nil {
		op, resource = "skill.release", c.ID
	} else if !errors.Is(e, fault.ErrNotFound) {
		return skills.Release{}, e
	}
	in := pinned{Name: man.Name, Version: man.Version, Digest: digest(raw)}
	ref := newID() // also its blob key: stores accept only lower-hex keys
	if err = m.d.Blobs.Put(ctx, ref, bytes.NewReader(raw)); err != nil {
		return skills.Release{}, err
	}
	applied := false
	out, err := command.Run(ctx, m.d.Commands, capcommand.Request{Token: token, Key: key, Operation: op, Resource: resource}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		now := time.Now().UTC()
		c, e := m.d.Registry.FindCapability(tx, p.Tenant, man.Name)
		switch {
		case errors.Is(e, fault.ErrNotFound):
			if op != "skill.create" {
				return nil, fault.ErrConflict // gone since the pre-check
			}
			c = skills.Capability{Tenant: p.Tenant, ID: newID(), Name: man.Name, Owner: p.ID, Visibility: skills.Private, CreatedAt: now}
			if e = m.d.Registry.CreateCapability(tx, c); e != nil {
				return nil, e
			}
		case e != nil:
			return nil, e
		case op != "skill.release" || c.ID != resource:
			return nil, fault.ErrConflict // created concurrently: retry as a release
		case c.Archived:
			return nil, fault.ErrConflict
		default:
			cur, e := m.d.Registry.CurrentSkillRelease(tx, p.Tenant, c.ID)
			if e != nil {
				return nil, e
			}
			if cur.Version == man.Version && cur.Digest == in.Digest {
				return json.Marshal(cur.Ref) // byte-identical retry: the existing release
			}
			if !skillpkg.Newer(man.Version, cur.Version) {
				return nil, &skills.Invalid{Diagnostics: []skills.Diagnostic{notNewer(cur.Version)}}
			}
		}
		r := skills.Release{
			Tenant: p.Tenant, CapabilityID: c.ID, Ref: ref, Name: man.Name, Description: man.Description, Version: man.Version,
			Digest: in.Digest, Size: int64(len(raw)), Declarations: man.Declarations, Publisher: p.ID, CreatedAt: now,
		}
		if e := m.d.Registry.AppendSkillRelease(tx, r); e != nil {
			return nil, e
		}
		applied = true
		return json.Marshal(ref)
	})
	if err != nil || !applied {
		// Compensate: these bytes are unregistered (failure) or not the
		// release's bytes (replay or identical retry). A failed delete
		// leaves only unreachable bytes.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if e := m.d.Blobs.Delete(cleanup, ref); e != nil {
			slog.Default().WarnContext(ctx, "compensating blob delete failed; bytes left unreachable", slog.String("key", ref), slog.String("error", e.Error()))
		}
		cancel()
	}
	if err != nil {
		return skills.Release{}, err
	}
	var stored string
	if err = json.Unmarshal(out, &stored); err != nil {
		return skills.Release{}, err
	}
	r, err := m.d.Registry.GetSkillRelease(ctx, caller.Tenant, stored)
	if err != nil {
		return skills.Release{}, err
	}
	if applied && m.d.Embedder != nil {
		m.index(ctx, r) // best effort; Reindex recovers a failure
	}
	return r, nil
}

func (m *Module) SetVisibility(ctx context.Context, token, key, id, visibility string) (skills.Capability, error) {
	if visibility != skills.Private && visibility != skills.Tenant {
		return skills.Capability{}, fault.ErrInput
	}
	return m.manage(ctx, token, key, id, "skill.visibility", visibility, func(c *skills.Capability) { c.Visibility = visibility })
}

func (m *Module) SetCollaborators(ctx context.Context, token, key, id string, subjects []string) (skills.Capability, error) {
	clean := []string{}
	for _, s := range subjects {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 128 {
			return skills.Capability{}, fault.ErrInput
		}
		if !slices.Contains(clean, s) {
			clean = append(clean, s)
		}
	}
	if len(clean) > maxCollaborator {
		return skills.Capability{}, fault.ErrInput
	}
	slices.Sort(clean)
	return m.manage(ctx, token, key, id, "skill.collaborators", clean, func(c *skills.Capability) {
		c.Collaborators = slices.DeleteFunc(slices.Clone(clean), func(s string) bool { return s == c.Owner })
	})
}

func (m *Module) SetArchived(ctx context.Context, token, key, id string, archived bool) (skills.Capability, error) {
	return m.manage(ctx, token, key, id, "skill.archive", archived, func(c *skills.Capability) { c.Archived = archived })
}

// manage changes owner-managed metadata under the skill.manage relation.
// The change name is part of the pinned input, so one key cannot replay another change.
func (m *Module) manage(ctx context.Context, token, key, id, name string, input any, change func(*skills.Capability)) (skills.Capability, error) {
	if id == "" || len(id) > 128 {
		return skills.Capability{}, fault.ErrInput
	}
	caller, err := m.authenticate(ctx, token)
	if err != nil {
		return skills.Capability{}, err
	}
	_, err = command.Run(ctx, m.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "skill.manage", Resource: id}, struct {
		Change string
		Input  any
	}{name, input}, func(tx context.Context, p identity.Principal) ([]byte, error) {
		c, e := m.d.Registry.GetCapability(tx, p.Tenant, id)
		if e != nil {
			return nil, e
		}
		change(&c)
		return nil, m.d.Registry.SaveCapability(tx, c)
	})
	if err != nil {
		return skills.Capability{}, err
	}
	return m.d.Registry.GetCapability(ctx, caller.Tenant, id)
}

// Reindex authorizes an administrator through the command lifecycle, which
// audits it; the receipt holds nothing. It then embeds, outside any
// transaction, every current release still without a vector, so a replay
// simply finds whatever is still missing.
func (m *Module) Reindex(ctx context.Context, token, key string) (int, error) {
	if m.d.Embedder == nil {
		return 0, fault.ErrUnavailable
	}
	caller, err := m.authenticate(ctx, token)
	if err != nil {
		return 0, err
	}
	_, err = command.Run(ctx, m.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "skill.admin", Resource: caller.Tenant}, "reindex", func(context.Context, identity.Principal) ([]byte, error) {
		return nil, nil
	})
	if err != nil {
		return 0, err
	}
	missing, err := m.d.Registry.MissingSkillVectors(ctx, caller.Tenant)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, r := range missing {
		if err = m.vector(ctx, r); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// current is the current release of a named capability.
func (m *Module) current(ctx context.Context, tenant, name string) (skills.Release, error) {
	c, err := m.d.Registry.FindCapability(ctx, tenant, name)
	if err != nil {
		return skills.Release{}, err
	}
	return m.d.Registry.CurrentSkillRelease(ctx, tenant, c.ID)
}

func (m *Module) index(ctx context.Context, r skills.Release) {
	_ = m.vector(context.WithoutCancel(ctx), r)
}

func (m *Module) vector(ctx context.Context, r skills.Release) error {
	ctx, cancel := context.WithTimeout(ctx, m.embed)
	defer cancel()
	vs, err := m.d.Embedder.Embed(ctx, skills.PurposeDocument, []string{r.Name + "\n" + r.Description})
	if err != nil {
		return err
	}
	if len(vs) != 1 || len(vs[0]) == 0 {
		return fault.ErrUnavailable
	}
	return m.d.Transactions.Do(ctx, func(tx context.Context) error {
		return m.d.Registry.SetSkillVector(tx, r.Tenant, r.Ref, vs[0])
	})
}

func (m *Module) authenticate(ctx context.Context, token string) (identity.Principal, error) {
	var p identity.Principal
	err := m.d.Transactions.Do(ctx, func(tx context.Context) error {
		var e error
		p, e = m.d.Identity.Authenticate(tx, token)
		return e
	})
	return p, err
}

func (m *Module) read(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, m.c.MaxBytes+1))
	if err != nil {
		return nil, fault.ErrInput
	}
	if int64(len(raw)) > m.c.MaxBytes {
		return nil, fault.ErrTooLarge
	}
	return raw, nil
}

func blocking(ds []skills.Diagnostic) bool {
	return slices.ContainsFunc(ds, func(d skills.Diagnostic) bool { return d.Blocking })
}

func notNewer(current string) skills.Diagnostic {
	return skills.Diagnostic{Code: "version-not-increasing", Message: "version must be greater than the current " + current, Blocking: true}
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return hex.EncodeToString(b[:])
}
