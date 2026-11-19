package sql

import (
	"context"
	"encoding/json"
	"github.com/lengzhao/taskloom/cap/checkpoint"
	"github.com/lengzhao/taskloom/cap/fault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type checkpointRow struct {
	Key  string `gorm:"primaryKey;size:64;column:row_key"`
	Data []byte
}

func checkpointKey(v checkpoint.Record) string {
	return key(v.Tenant, v.TaskID, v.RoundID, v.SubmissionID)
}
func (s *Store) EnsureCheckpoint(ctx context.Context, v checkpoint.Record) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&checkpointRow{Key: checkpointKey(v), Data: b}).Error
	})
}
func (s *Store) ReadCheckpoint(ctx context.Context, v checkpoint.Record) (checkpoint.Record, error) {
	var out checkpoint.Record
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var row checkpointRow
		if e := db.First(&row, "row_key = ?", checkpointKey(v)).Error; e != nil {
			return e
		}
		return json.Unmarshal(row.Data, &out)
	})
	return out, e
}
func (s *Store) ConfirmCheckpoint(ctx context.Context, v checkpoint.Record) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		r := db.Model(&checkpointRow{}).Where("row_key = ?", checkpointKey(v)).Update("data", b)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fault.ErrConflict
		}
		return nil
	})
}
