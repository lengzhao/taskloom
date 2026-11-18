package sql

import (
	"context"
	"encoding/json"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"gorm.io/gorm"
)

type positionHistoryRow struct {
	Key  string `gorm:"primaryKey;size:64;column:row_key"`
	Data []byte
}
type positionRow struct {
	Key  string `gorm:"primaryKey;size:64;column:row_key"`
	Data []byte
}

func (s *Store) CreatePosition(ctx context.Context, p flow.Position) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(p)
		if e != nil {
			return e
		}
		return db.Create(&positionRow{Key: key(p.Tenant, p.TaskID), Data: b}).Error
	})
}
func (s *Store) GetPosition(ctx context.Context, tenant, id string) (flow.Position, error) {
	var p flow.Position
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var r positionRow
		if e := db.First(&r, "row_key = ?", key(tenant, id)).Error; e != nil {
			return e
		}
		return json.Unmarshal(r.Data, &p)
	})
	return p, err
}

// SavePosition is a compare-and-swap: the stored revision must equal expected,
// and the UPDATE matches the exact bytes read, so a concurrent writer that
// slipped in between is rejected even without the global coordination lock.
func (s *Store) SavePosition(ctx context.Context, p flow.Position, expected int64) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		var old positionRow
		if e := db.First(&old, "row_key = ?", key(p.Tenant, p.TaskID)).Error; e != nil {
			return e
		}
		var stored flow.Position
		if e := json.Unmarshal(old.Data, &stored); e != nil {
			return e
		}
		if stored.Revision != expected || p.Revision <= expected {
			return fault.ErrConflict
		}
		b, e := json.Marshal(p)
		if e != nil {
			return e
		}
		r := db.Model(&positionRow{}).Where("row_key = ? AND data = ?", key(p.Tenant, p.TaskID), old.Data).Update("data", b)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fault.ErrConflict
		}
		return db.Create(&positionHistoryRow{Key: randomID(), Data: old.Data}).Error
	})
}

type flowDefinitionRow struct {
	Key    string `gorm:"primaryKey;size:64;column:row_key"`
	Digest string `gorm:"size:64"`
}

func (s *Store) EnsureDefinition(ctx context.Context, ref flow.DefinitionRef) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		var row flowDefinitionRow
		r := db.Where("row_key = ?", key(ref.ID, ref.Version)).Find(&row)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 1 {
			if row.Digest != ref.Digest {
				return fault.ErrConflict
			}
			return nil
		}
		return db.Create(&flowDefinitionRow{Key: key(ref.ID, ref.Version), Digest: ref.Digest}).Error
	})
}
