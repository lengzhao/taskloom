// Package query discovers skill capabilities, reads exact releases and their
// bytes, and decides delivery for a declared host. Every read rechecks the
// caller's tenant membership and the capability's visibility; an invisible,
// archived or missing capability is the same fault.ErrNotFound.
package query

import (
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/blob"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/skills"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/domain/skilldelivery"
	"github.com/lengzhao/taskloom/domain/skillmatch"
)

type Config struct {
	// Modes are the enabled match modes besides browsing; empty enables
	// name, lexical and bm25, plus semantic when an embedder is wired.
	Modes []string `json:"modes"`
	// MaxDistance drops semantic matches whose cosine distance exceeds it.
	MaxDistance float64 `json:"maxDistance"`
	// LexicalBonus is added to a semantic score when the text names every query token.
	LexicalBonus float64 `json:"lexicalBonus"`
	// EmbedTimeout bounds one query embedding.
	EmbedTimeout string `json:"embedTimeout"`
}

const (
	defaultSize = 24
	maxSize     = 100
	maxQuery    = 256
)

func (c *Config) SetDefaults() {
	if c.MaxDistance == 0 {
		c.MaxDistance = 0.65
	}
	if c.LexicalBonus == 0 {
		c.LexicalBonus = 0.1
	}
	if c.EmbedTimeout == "" {
		c.EmbedTimeout = "5s"
	}
}
func (c *Config) Validate() error {
	if c.MaxDistance <= 0 || c.MaxDistance > 2 {
		return errors.New("maxDistance must be in (0, 2]")
	}
	if c.LexicalBonus < 0 || c.LexicalBonus > 1 {
		return errors.New("lexicalBonus must be in [0, 1]")
	}
	if d, err := time.ParseDuration(c.EmbedTimeout); err != nil || d <= 0 {
		return errors.New("embedTimeout must be a positive duration")
	}
	for _, mode := range c.Modes {
		if !slices.Contains(allModes, mode) {
			return errors.New("modes: unknown match mode " + mode)
		}
	}
	return nil
}

var allModes = []string{skills.MatchName, skills.MatchLexical, skills.MatchBM25, skills.MatchSemantic}

// Deps has six fields, reviewed: every read authenticates (identity) and
// checks membership and administrator relations (authorization) in a read
// transaction (transactions) over the catalog (catalog); artifact reads then
// open bytes (blobs) and semantic search encodes the query (embedder, optional),
// both outside the transaction. Splitting reads by kind would duplicate the check.
type Deps struct {
	Transactions  unitofwork.Viewer      `json:"transactions"`
	Identity      identity.Authenticator `json:"identity"`
	Authorization az.Authorizer          `json:"authorization"`
	Catalog       skills.Catalog         `json:"catalog"`
	Blobs         blob.Store             `json:"blobs"`
	Embedder      skills.Embedder        `json:"embedder,omitempty"`
}
type Module struct {
	c     Config
	d     Deps
	embed time.Duration
	modes []string
}

func init() { pluginkit.Register("query/skills", New) }
func New(c Config, d Deps) (*Module, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if d.Transactions == nil || d.Identity == nil || d.Authorization == nil || d.Catalog == nil || d.Blobs == nil {
		return nil, errors.New("skill query capabilities required")
	}
	modes := c.Modes
	if len(modes) == 0 {
		modes = []string{skills.MatchName, skills.MatchLexical, skills.MatchBM25}
		if d.Embedder != nil {
			modes = append(modes, skills.MatchSemantic)
		}
	}
	if slices.Contains(modes, skills.MatchSemantic) && d.Embedder == nil {
		return nil, errors.New("semantic match mode needs an embedder")
	}
	t, _ := time.ParseDuration(c.EmbedTimeout)
	return &Module{c: c, d: d, embed: t, modes: modes}, nil
}

// reader is the authenticated caller and whether it administers the tenant.
type reader struct {
	identity.Principal
	admin bool
}

func (m *Module) reader(tx context.Context, token string) (reader, error) {
	p, err := m.d.Identity.Authenticate(tx, token)
	if err != nil {
		return reader{}, err
	}
	if err = m.d.Authorization.Check(tx, p, "skill.read", p.Tenant); err != nil {
		return reader{}, err
	}
	err = m.d.Authorization.Check(tx, p, "skill.admin", p.Tenant)
	if err != nil && !errors.Is(err, az.ErrForbidden) {
		return reader{}, err
	}
	return reader{p, err == nil}, nil
}

func (r reader) sees(c skills.Capability) bool {
	return !c.Archived && c.VisibleTo(r.ID, r.admin)
}

func (m *Module) Search(ctx context.Context, token string, q skills.Query) (skills.Page, error) {
	q.Q = strings.TrimSpace(q.Q)
	if err := validQuery(&q); err != nil {
		return skills.Page{}, err
	}
	if q.Match != "" && !slices.Contains(m.modes, q.Match) {
		return skills.Page{}, fault.ErrInput // not enabled in this deployment
	}
	var visible []skills.Current
	var vectors map[string][]float32
	trust := map[string]string{}
	err := m.d.Transactions.View(ctx, func(tx context.Context) error {
		r, e := m.reader(tx, token)
		if e != nil {
			return e
		}
		all, e := m.d.Catalog.ListCapabilities(tx, r.Tenant)
		if e != nil {
			return e
		}
		for _, c := range all {
			if !r.sees(c.Capability) {
				continue
			}
			var latest *skills.Review
			v, e := m.d.Catalog.LatestSkillReview(tx, r.Tenant, c.Release.Ref)
			if e == nil {
				latest = &v
			} else if !errors.Is(e, fault.ErrNotFound) {
				return e
			}
			t := skilldelivery.Trust(latest)
			if q.Trust != "" && t != q.Trust {
				continue
			}
			trust[c.Release.Ref] = t
			visible = append(visible, c)
		}
		if q.Match == skills.MatchSemantic {
			refs := make([]string, len(visible))
			for i, c := range visible {
				refs[i] = c.Release.Ref
			}
			vectors, e = m.d.Catalog.SkillVectors(tx, r.Tenant, refs)
		}
		return e
	})
	if err != nil {
		return skills.Page{}, err
	}
	var query []float32
	if q.Match == skills.MatchSemantic {
		// Remote IO only after authorization and outside the transaction.
		ectx, cancel := context.WithTimeout(ctx, m.embed)
		vs, e := m.d.Embedder.Embed(ectx, skills.PurposeQuery, []string{q.Q})
		cancel()
		if e != nil || len(vs) != 1 || len(vs[0]) == 0 {
			return skills.Page{}, fault.ErrUnavailable // no silent lexical fallback
		}
		query = vs[0]
	}
	texts := make([]skillmatch.Text, len(visible))
	for i, c := range visible {
		texts[i] = skillmatch.Text{Name: c.Name, Description: c.Release.Description}
	}
	var bm25 []float64
	if q.Match == skills.MatchBM25 {
		bm25 = skillmatch.BM25(texts, q.Q)
	}
	items := []skills.Candidate{}
	for i, c := range visible {
		score, ok := 0.0, true
		text := texts[i]
		switch q.Match {
		case skills.MatchBM25:
			score = bm25[i]
			ok = score > 0
		case skills.MatchName:
			score, ok = skillmatch.Name(c.Name, q.Q)
		case skills.MatchLexical:
			score, ok = skillmatch.Lexical(text, q.Q)
		case skills.MatchSemantic:
			v, has := vectors[c.Release.Ref]
			ok = has
			if has {
				score, ok = skillmatch.Semantic(skillmatch.Cosine(query, v), skillmatch.Mentions(text, q.Q), m.c.MaxDistance, m.c.LexicalBonus)
			}
		}
		if ok && score >= q.MinScore {
			items = append(items, skills.Candidate{Current: c, Score: score, Trust: trust[c.Release.Ref]})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].Name < items[j].Name
	})
	total := len(items)
	start := min((q.Page-1)*q.Size, total)
	return skills.Page{Items: items[start:min(start+q.Size, total)], Total: total}, nil
}

func validQuery(q *skills.Query) error {
	if q.Page == 0 {
		q.Page = 1
	}
	if q.Size == 0 {
		q.Size = defaultSize
	}
	if q.Page < 1 || q.Page > 10000 || q.Size < 1 || q.Size > maxSize || q.MinScore < 0 || q.MinScore > 1 || utf8.RuneCountInString(q.Q) > maxQuery {
		return fault.ErrInput
	}
	switch q.Trust {
	case "", skills.TrustApproved, skills.TrustRejected, skills.TrustUnreviewed:
	default:
		return fault.ErrInput
	}
	switch q.Match {
	case "":
		if q.Q != "" {
			return fault.ErrInput
		}
	case skills.MatchName:
		if q.Q == "" {
			return fault.ErrInput
		}
	case skills.MatchLexical, skills.MatchBM25, skills.MatchSemantic:
		// One character embeds near the neutral vector and matches noise.
		if utf8.RuneCountInString(q.Q) < 2 {
			return fault.ErrInput
		}
	default:
		return fault.ErrInput
	}
	return nil
}

func (m *Module) Resolve(ctx context.Context, token, name string) (skills.ReleaseInfo, error) {
	if name == "" || len(name) > 64 {
		return skills.ReleaseInfo{}, fault.ErrInput
	}
	var out skills.ReleaseInfo
	err := m.d.Transactions.View(ctx, func(tx context.Context) error {
		r, e := m.reader(tx, token)
		if e != nil {
			return e
		}
		c, e := m.d.Catalog.FindCapability(tx, r.Tenant, name)
		if e != nil {
			return e
		}
		if !r.sees(c) {
			return fault.ErrNotFound
		}
		cur, e := m.d.Catalog.CurrentSkillRelease(tx, r.Tenant, c.ID)
		if e != nil {
			return e
		}
		out, e = m.info(tx, r.Tenant, cur.Ref)
		return e
	})
	return out, err
}

func (m *Module) Release(ctx context.Context, token, ref string) (skills.ReleaseInfo, error) {
	var out skills.ReleaseInfo
	err := m.d.Transactions.View(ctx, func(tx context.Context) error {
		var e error
		out, e = m.visibleRelease(tx, token, ref)
		return e
	})
	return out, err
}

// Open checks visibility, then opens the bytes outside the SQL transaction.
func (m *Module) Open(ctx context.Context, token, ref string) (skills.Release, io.ReadCloser, error) {
	var info skills.ReleaseInfo
	err := m.d.Transactions.View(ctx, func(tx context.Context) error {
		var e error
		info, e = m.visibleRelease(tx, token, ref)
		return e
	})
	if err != nil {
		return skills.Release{}, nil, err
	}
	body, err := m.d.Blobs.Get(ctx, info.Ref)
	if errors.Is(err, fault.ErrNotFound) {
		err = fault.ErrUnavailable // registered release, missing bytes: a storage failure
	}
	if err != nil {
		return skills.Release{}, nil, err
	}
	return info.Release, body, nil
}

func (m *Module) Decide(ctx context.Context, token, ref string, host skills.HostFacts) (skills.Decision, error) {
	if len(host.Contract) > 128 || len(host.Affordances) > 32 || len(host.Apps) > 64 {
		return skills.Decision{}, fault.ErrInput
	}
	var r skills.Release
	var latest *skills.Review
	err := m.d.Transactions.View(ctx, func(tx context.Context) error {
		info, e := m.visibleRelease(tx, token, ref)
		if e != nil {
			return e
		}
		r = info.Release
		v, e := m.d.Catalog.LatestSkillReview(tx, r.Tenant, ref)
		if e == nil {
			latest = &v
		} else if !errors.Is(e, fault.ErrNotFound) {
			return e
		}
		return nil
	})
	if err != nil {
		return skills.Decision{}, err
	}
	return skilldelivery.Decide(r, latest, host), nil
}

func (m *Module) visibleRelease(tx context.Context, token, ref string) (skills.ReleaseInfo, error) {
	if ref == "" || len(ref) > 128 {
		return skills.ReleaseInfo{}, fault.ErrInput
	}
	r, err := m.reader(tx, token)
	if err != nil {
		return skills.ReleaseInfo{}, err
	}
	info, err := m.info(tx, r.Tenant, ref)
	if err != nil {
		return skills.ReleaseInfo{}, err
	}
	c, err := m.d.Catalog.GetCapability(tx, r.Tenant, info.CapabilityID)
	if err != nil {
		return skills.ReleaseInfo{}, err
	}
	if !r.sees(c) {
		return skills.ReleaseInfo{}, fault.ErrNotFound
	}
	return info, nil
}

func (m *Module) info(tx context.Context, tenant, ref string) (skills.ReleaseInfo, error) {
	rel, err := m.d.Catalog.GetSkillRelease(tx, tenant, ref)
	if err != nil {
		return skills.ReleaseInfo{}, err
	}
	var latest *skills.Review
	v, err := m.d.Catalog.LatestSkillReview(tx, tenant, ref)
	if err == nil {
		latest = &v
	} else if !errors.Is(err, fault.ErrNotFound) {
		return skills.ReleaseInfo{}, err
	}
	return skills.ReleaseInfo{Release: rel, Trust: skilldelivery.Trust(latest)}, nil
}
