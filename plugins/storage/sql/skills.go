package sql

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/skills"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// skillCapabilityRow is one capability. NameKey makes the name unique within
// the tenant; Archived filters discovery.
type skillCapabilityRow struct {
	Key      string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant   string `gorm:"size:128;index"`
	NameKey  string `gorm:"size:64;uniqueIndex"`
	Archived bool
	Data     []byte
}

// skillReleaseRow is one immutable release; VersionKey refuses a second
// release of one version of a capability. Sequence is the global commit
// order, so a capability's current release is its highest sequence.
type skillReleaseRow struct {
	Key           string             `gorm:"primaryKey;size:64;column:row_key"`
	CapabilityKey string             `gorm:"size:64;not null;index:idx_skill_release_order,priority:1"`
	Capability    skillCapabilityRow `gorm:"foreignKey:CapabilityKey;references:Key;constraint:OnDelete:RESTRICT"`
	Sequence      int64              `gorm:"index:idx_skill_release_order,priority:2"`
	VersionKey    string             `gorm:"size:64;uniqueIndex"`
	Data          []byte
}

// skillReviewRow is an append-only review; Sequence orders reviews of a release.
type skillReviewRow struct {
	Key        string          `gorm:"primaryKey;size:64;column:row_key"`
	ReleaseKey string          `gorm:"size:64;not null;index"`
	Release    skillReleaseRow `gorm:"foreignKey:ReleaseKey;references:Key;constraint:OnDelete:RESTRICT"`
	Sequence   int64           `gorm:"index"`
	Data       []byte
}

// skillVectorRow is the derived discovery vector of a current release (same
// key). It is written after the release commits, dropped once superseded and
// rebuilt by reindexing.
type skillVectorRow struct {
	Key           string `gorm:"primaryKey;size:64;column:row_key"`
	CapabilityKey string `gorm:"size:64;index"`
	Data          []byte
}

func capabilityKey(tenant, id string) string { return key(tenant, "skill", id) }
func releaseKey(tenant, ref string) string   { return key(tenant, "skill-release", ref) }

// latestRelease keeps, per capability, only the release with the highest sequence.
func latestRelease(db *gorm.DB) *gorm.DB {
	return db.Where("NOT EXISTS (SELECT 1 FROM skill_release_rows n WHERE n.capability_key = skill_release_rows.capability_key AND n.sequence > skill_release_rows.sequence)")
}

func (s *Store) GetCapability(ctx context.Context, tenant, id string) (skills.Capability, error) {
	var c skills.Capability
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var row skillCapabilityRow
		if err := db.First(&row, "row_key = ?", capabilityKey(tenant, id)).Error; err != nil {
			return err
		}
		return decodeCapability(row, &c)
	})
	return c, err
}

func (s *Store) FindCapability(ctx context.Context, tenant, name string) (skills.Capability, error) {
	var c skills.Capability
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var row skillCapabilityRow
		if err := db.First(&row, "name_key = ?", key(tenant, "skill-name", name)).Error; err != nil {
			return err
		}
		return decodeCapability(row, &c)
	})
	return c, err
}

func (s *Store) ListCapabilities(ctx context.Context, tenant string) ([]skills.Current, error) {
	out := []skills.Current{}
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var rows []skillCapabilityRow
		if err := db.Where("tenant = ? AND archived = ?", tenant, false).Find(&rows).Error; err != nil {
			return err
		}
		keys := make([]string, len(rows))
		for i, row := range rows {
			keys[i] = row.Key
		}
		current := map[string]skills.Release{}
		for start := 0; start < len(keys); start += 500 {
			var rels []skillReleaseRow
			if err := latestRelease(db.Omit("Capability").Where("capability_key IN ?", keys[start:min(start+500, len(keys))])).Find(&rels).Error; err != nil {
				return err
			}
			for _, rel := range rels {
				var r skills.Release
				if err := json.Unmarshal(rel.Data, &r); err != nil {
					return err
				}
				current[rel.CapabilityKey] = r
			}
		}
		for _, row := range rows {
			r, ok := current[row.Key]
			if !ok {
				continue // created and released in one command; never without a release
			}
			var c skills.Capability
			if err := decodeCapability(row, &c); err != nil {
				return err
			}
			out = append(out, skills.Current{Capability: c, Release: r})
		}
		return nil
	})
	return out, err
}

func (s *Store) CreateCapability(ctx context.Context, c skills.Capability) error {
	row, err := capabilityRow(c)
	if err != nil {
		return err
	}
	return s.use(ctx, true, func(db *gorm.DB) error { return db.Create(&row).Error })
}

func (s *Store) SaveCapability(ctx context.Context, c skills.Capability) error {
	row, err := capabilityRow(c)
	if err != nil {
		return err
	}
	return s.use(ctx, true, func(db *gorm.DB) error {
		// The name never changes, so only the archive state and data move.
		res := db.Model(&skillCapabilityRow{}).Where("row_key = ?", row.Key).Updates(map[string]any{"archived": row.Archived, "data": row.Data})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fault.ErrNotFound
		}
		return nil
	})
}

func capabilityRow(c skills.Capability) (skillCapabilityRow, error) {
	if c.Tenant == "" || c.ID == "" || c.Name == "" {
		return skillCapabilityRow{}, fault.ErrInput
	}
	b, err := json.Marshal(c)
	return skillCapabilityRow{Key: capabilityKey(c.Tenant, c.ID), Tenant: c.Tenant, NameKey: key(c.Tenant, "skill-name", c.Name), Archived: c.Archived, Data: b}, err
}

func (s *Store) CurrentSkillRelease(ctx context.Context, tenant, capabilityID string) (skills.Release, error) {
	var r skills.Release
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var row skillReleaseRow
		if err := db.Omit("Capability").Where("capability_key = ?", capabilityKey(tenant, capabilityID)).Order("sequence DESC").First(&row).Error; err != nil {
			return err
		}
		return json.Unmarshal(row.Data, &r)
	})
	return r, err
}

func (s *Store) AppendSkillRelease(ctx context.Context, r skills.Release) error {
	if r.Tenant == "" || r.Ref == "" || r.CapabilityID == "" || r.Version == "" {
		return fault.ErrInput
	}
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		seq, err := nextSequence(db)
		if err != nil {
			return err
		}
		ck := capabilityKey(r.Tenant, r.CapabilityID)
		if err = db.Omit("Capability").Create(&skillReleaseRow{
			Key: releaseKey(r.Tenant, r.Ref), CapabilityKey: ck, Sequence: seq,
			VersionKey: key(r.Tenant, r.CapabilityID, "version", r.Version), Data: b,
		}).Error; err != nil {
			return err
		}
		// Discovery reads only the current release's vector.
		return db.Where("capability_key = ?", ck).Delete(&skillVectorRow{}).Error
	})
}

func (s *Store) GetSkillRelease(ctx context.Context, tenant, ref string) (skills.Release, error) {
	var r skills.Release
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var row skillReleaseRow
		if err := db.Omit("Capability").First(&row, "row_key = ?", releaseKey(tenant, ref)).Error; err != nil {
			return err
		}
		return json.Unmarshal(row.Data, &r)
	})
	return r, err
}

func (s *Store) AppendSkillReview(ctx context.Context, v skills.Review) error {
	if v.Tenant == "" || v.Ref == "" || v.Reviewer == "" {
		return fault.ErrInput
	}
	return s.use(ctx, true, func(db *gorm.DB) error {
		seq, err := nextSequence(db)
		if err != nil {
			return err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return db.Omit("Release").Create(&skillReviewRow{Key: randomID(), ReleaseKey: releaseKey(v.Tenant, v.Ref), Sequence: seq, Data: b}).Error
	})
}

func (s *Store) LatestSkillReview(ctx context.Context, tenant, ref string) (skills.Review, error) {
	var v skills.Review
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var row skillReviewRow
		if err := db.Omit("Release").Where("release_key = ?", releaseKey(tenant, ref)).Order("sequence DESC").First(&row).Error; err != nil {
			return err
		}
		return json.Unmarshal(row.Data, &v)
	})
	return v, err
}

func (s *Store) SetSkillVector(ctx context.Context, tenant, ref string, vector []float32) error {
	if len(vector) == 0 {
		return fault.ErrInput
	}
	return s.use(ctx, true, func(db *gorm.DB) error {
		var rel skillReleaseRow
		if err := latestRelease(db.Omit("Capability").Select("row_key", "capability_key")).First(&rel, "row_key = ?", releaseKey(tenant, ref)).Error; err != nil {
			return err // missing, or superseded: only a current release is indexed
		}
		return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&skillVectorRow{Key: rel.Key, CapabilityKey: rel.CapabilityKey, Data: encodeVector(vector)}).Error
	})
}

func (s *Store) SkillVectors(ctx context.Context, tenant string, refs []string) (map[string][]float32, error) {
	out := map[string][]float32{}
	err := s.use(ctx, false, func(db *gorm.DB) error {
		byKey := map[string]string{}
		keys := make([]string, 0, len(refs))
		for _, ref := range refs {
			k := releaseKey(tenant, ref)
			byKey[k] = ref
			keys = append(keys, k)
		}
		for start := 0; start < len(keys); start += 500 {
			end := min(start+500, len(keys))
			var rows []skillVectorRow
			if err := db.Where("row_key IN ?", keys[start:end]).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				v, err := decodeVector(row.Data)
				if err != nil {
					return err
				}
				out[byKey[row.Key]] = v
			}
		}
		return nil
	})
	return out, err
}

func (s *Store) MissingSkillVectors(ctx context.Context, tenant string) ([]skills.Release, error) {
	current, err := s.ListCapabilities(ctx, tenant)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(current))
	for _, c := range current {
		refs = append(refs, c.Release.Ref)
	}
	have, err := s.SkillVectors(ctx, tenant, refs)
	if err != nil {
		return nil, err
	}
	out := []skills.Release{}
	for _, c := range current {
		if _, ok := have[c.Release.Ref]; !ok {
			out = append(out, c.Release)
		}
	}
	return out, nil
}

func decodeCapability(row skillCapabilityRow, c *skills.Capability) error {
	if err := json.Unmarshal(row.Data, c); err != nil {
		return err
	}
	c.Archived = row.Archived
	return nil
}

func encodeVector(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, errors.New("corrupt vector")
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
