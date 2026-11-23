// Package skills defines the skill delivery control plane: immutable Agent
// Skill releases, their discovery, and the delivery decision for one exact
// release on one declared host. It never executes a skill; which skills a
// host adopts belongs to that host. Consumer-side loading is AgentKit's
// cap/skill, a different package.
package skills

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/lengzhao/taskloom/cap/fault"
)

// Visibility is an explicit audience within the capability's tenant.
const (
	Private = "private" // owner and collaborators
	Tenant  = "tenant"  // every active member of the tenant
)

// Capability is the stable semantic name and its owner-managed metadata.
// Name and tenant never change. Its current release is not stored: versions
// strictly increase, so it is the last release appended.
type Capability struct {
	Tenant, ID, Name string
	Owner            string
	Visibility       string
	Collaborators    []string `json:",omitempty"`
	Archived         bool
	CreatedAt        time.Time
}

// VisibleTo reports whether subject may read the capability. Administrators
// see every capability of their tenant.
func (c Capability) VisibleTo(subject string, admin bool) bool {
	return admin || c.Visibility == Tenant || c.Owner == subject || slices.Contains(c.Collaborators, subject)
}

// Contributes reports whether subject may publish new releases.
func (c Capability) Contributes(subject string) bool {
	return c.Owner == subject || slices.Contains(c.Collaborators, subject)
}

// Declarations are what a release states about itself in SKILL.md
// metadata.skilla. They are evidence for decisions, not grants.
type Declarations struct {
	RequiredApps    []string `json:",omitempty"`
	EgressHosts     []string `json:",omitempty"`
	WhenToUse       []string `json:",omitempty"`
	NotFor          []string `json:",omitempty"`
	SuccessCriteria []string `json:",omitempty"`
	Examples        []string `json:",omitempty"`
}

// Diagnostic is one deterministic finding about an archive. A blocking one
// rejects the publication; the others are advisory. Diagnostics are not
// stored: they follow from the immutable bytes and are shown at preflight.
type Diagnostic struct {
	Code     string
	Message  string
	Blocking bool `json:",omitempty"`
}

// Release is one immutable version. Ref is opaque and also keys its bytes;
// Digest is the SHA-256 of the exact published archive in hex.
type Release struct {
	Tenant, CapabilityID, Ref  string
	Name, Description, Version string
	Digest                     string
	Size                       int64
	Declarations               Declarations
	// Publisher is the authenticated principal; reviews must not be its own.
	Publisher string
	CreatedAt time.Time
}

// Current is a capability with its current release.
type Current struct {
	Capability
	Release Release
}

// Review verdicts. A review never changes visibility or content.
const (
	Approved = "approved"
	Rejected = "rejected"
)

// Review is an append-only independent trust event for one exact release.
type Review struct {
	Tenant, Ref, Verdict, Reason, Reviewer string
	CreatedAt                              time.Time
}

// CapabilityReader is what authorization needs to judge capability relations.
type CapabilityReader interface {
	GetCapability(ctx context.Context, tenant, id string) (Capability, error)
}

// Registry is the publication command's storage. Every write transaction
// holds the store's global lock, so a command's read and write cannot
// interleave with another. CreateCapability refuses an existing id or name
// (fault.ErrConflict); SaveCapability replaces the metadata of an existing
// one. AppendSkillRelease refuses a second release of one version and drops
// the discovery vectors of the capability's superseded releases, which no
// reader uses. Releases and reviews are never updated or deleted.
type Registry interface {
	CapabilityReader
	FindCapability(ctx context.Context, tenant, name string) (Capability, error)
	CreateCapability(ctx context.Context, value Capability) error
	SaveCapability(ctx context.Context, value Capability) error
	// CurrentSkillRelease is the last release appended, fault.ErrNotFound if none.
	CurrentSkillRelease(ctx context.Context, tenant, capabilityID string) (Release, error)
	AppendSkillRelease(ctx context.Context, value Release) error
	GetSkillRelease(ctx context.Context, tenant, ref string) (Release, error)
	// SetSkillVector stores the discovery vector of a release, replacing any.
	SetSkillVector(ctx context.Context, tenant, ref string, vector []float32) error
	// MissingSkillVectors lists current releases of active capabilities that have none.
	MissingSkillVectors(ctx context.Context, tenant string) ([]Release, error)
}

// Catalog is read-only discovery storage. ListCapabilities returns the
// tenant's active capabilities with their current releases.
type Catalog interface {
	CapabilityReader
	ListCapabilities(ctx context.Context, tenant string) ([]Current, error)
	FindCapability(ctx context.Context, tenant, name string) (Capability, error)
	CurrentSkillRelease(ctx context.Context, tenant, capabilityID string) (Release, error)
	GetSkillRelease(ctx context.Context, tenant, ref string) (Release, error)
	// LatestSkillReview is fault.ErrNotFound when a release has no review.
	LatestSkillReview(ctx context.Context, tenant, ref string) (Review, error)
	SkillVectors(ctx context.Context, tenant string, refs []string) (map[string][]float32, error)
}

// Reviews is the review command's storage.
type Reviews interface {
	GetSkillRelease(ctx context.Context, tenant, ref string) (Release, error)
	AppendSkillReview(ctx context.Context, value Review) error
}

// Embedding purposes: a query and a document may be encoded differently.
const (
	PurposeQuery    = "query"
	PurposeDocument = "document"
)

// Embedder turns texts into vectors of one fixed dimension. It is remote IO:
// callers never invoke it inside a transaction.
type Embedder interface {
	Embed(ctx context.Context, purpose string, texts []string) ([][]float32, error)
}

// Invalid rejects a publication with its deterministic diagnostics.
type Invalid struct{ Diagnostics []Diagnostic }

func (e *Invalid) Error() string {
	codes := make([]string, 0, len(e.Diagnostics))
	for _, d := range e.Diagnostics {
		if d.Blocking {
			codes = append(codes, d.Code)
		}
	}
	return "invalid skill: " + strings.Join(codes, ",")
}
func (e *Invalid) Is(target error) bool { return target == fault.ErrInput }

// AsInvalid extracts publication diagnostics from an error chain.
func AsInvalid(err error) (*Invalid, bool) {
	var v *Invalid
	ok := errors.As(err, &v)
	return v, ok
}

// Report is a read-only preflight of the exact bytes a publication would send.
type Report struct {
	Name, Version, Digest string
	Size                  int64
	Publishable           bool
	Diagnostics           []Diagnostic
}

// Commands publish and manage capabilities. Publish creates the capability on
// its first release; later releases need a strictly greater version, and the
// same version with the same bytes returns the existing release.
type Commands interface {
	Preflight(ctx context.Context, token string, body io.Reader) (Report, error)
	Publish(ctx context.Context, token, key string, body io.Reader) (Release, error)
	SetVisibility(ctx context.Context, token, key, capabilityID, visibility string) (Capability, error)
	SetCollaborators(ctx context.Context, token, key, capabilityID string, subjects []string) (Capability, error)
	SetArchived(ctx context.Context, token, key, capabilityID string, archived bool) (Capability, error)
	// Reindex embeds current releases that have no vector and reports how
	// many; without an embedder it is fault.ErrUnavailable.
	Reindex(ctx context.Context, token, key string) (int, error)
}

// ReviewCommands append independent reviews; a publisher never reviews its own release.
type ReviewCommands interface {
	Review(ctx context.Context, token, key, ref, verdict, reason string) (Review, error)
}

// Match modes of discovery. Empty browses by name; a deployment enables
// the others it wants, and semantic needs an embedder.
const (
	MatchName     = "name"     // fuzzy: prefix or trigram on the name
	MatchLexical  = "lexical"  // keyword: names, description and declared use
	MatchBM25     = "bm25"     // Okapi BM25 over names, description and declared use
	MatchSemantic = "semantic" // vector similarity plus a lexical bonus
)

// Query pages discovery. Score is comparable only within one response.
type Query struct {
	Q, Match   string
	Page, Size int
	MinScore   float64
}
type Candidate struct {
	Current
	Score float64 `json:",omitempty"`
}
type Page struct {
	Items []Candidate
	Total int
}

// Trust of a release, derived from its latest review.
const (
	TrustApproved   = "approved"
	TrustRejected   = "rejected"
	TrustUnreviewed = "unreviewed"
)

// ReleaseInfo is an exact release with its current trust.
type ReleaseInfo struct {
	Release
	Trust string
}

// HostFacts is what a host declares about itself; the decision never infers
// compatibility from a host name.
type HostFacts struct {
	Contract    string   // e.g. agent-skill-host@1.0.0
	Affordances []string // e.g. agent-skills, filesystem, outbound-network
	Apps        []string // connected app keys available to the host
}

// Decision states: exactly one, with at most one action.
const (
	Ready          = "ready"
	ActionRequired = "action_required"
	Blocked        = "blocked"
	Unavailable    = "unavailable"
)

// Action is the one typed next step of a decision.
type Action struct {
	Kind   string   // materialize-exact-artifact | connect-apps
	Ref    string   `json:",omitempty"`
	Digest string   `json:",omitempty"`
	Size   int64    `json:",omitempty"`
	Apps   []string `json:",omitempty"`
}
type Decision struct {
	Ref, Name, Version string
	State              string
	Reason             string `json:",omitempty"`
	Trust              string
	Missing            []string `json:",omitempty"`
	Action             *Action  `json:",omitempty"`
}

// Queries read only what the caller may see; an invisible or archived
// capability is fault.ErrNotFound, as is a missing one.
type Queries interface {
	Search(ctx context.Context, token string, q Query) (Page, error)
	Resolve(ctx context.Context, token, name string) (ReleaseInfo, error)
	Release(ctx context.Context, token, ref string) (ReleaseInfo, error)
	Open(ctx context.Context, token, ref string) (Release, io.ReadCloser, error)
	Decide(ctx context.Context, token, ref string, host HostFacts) (Decision, error)
}
