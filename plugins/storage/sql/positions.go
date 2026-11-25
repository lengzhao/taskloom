package sql

import (
	"context"
	"encoding/json"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"gorm.io/gorm"
)

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
		if e := json.Unmarshal(r.Data, &p); e != nil {
			return e
		}
		p.Tenant = tenant // the key's, never part of the stored JSON
		return nil
	})
	return p, err
}

// SavePosition replaces the stored position. All writes hold the global
// coordination lock, so the row cannot change between a command's read and write.
func (s *Store) SavePosition(ctx context.Context, p flow.Position) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(p)
		if e != nil {
			return e
		}
		r := db.Model(&positionRow{}).Where("row_key = ?", key(p.Tenant, p.TaskID)).Update("data", b)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fault.ErrNotFound
		}
		return nil
	})
}
