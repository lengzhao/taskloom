package sql

import (
	"context"
	"encoding/json"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/task"
	"gorm.io/gorm"
)

type taskRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant  string `gorm:"size:128;index"`
	ID      string `gorm:"size:128"`
	Version int64
	Data    []byte
}

func (s *Store) CreateTask(ctx context.Context, t task.Task) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		return db.Create(&taskRow{Key: key(t.Tenant, t.ID), Tenant: t.Tenant, ID: t.ID, Version: t.Version, Data: b}).Error
	})
}
func (s *Store) GetTask(ctx context.Context, tenant, id string) (task.Task, error) {
	var r taskRow
	var t task.Task
	err := s.use(ctx, false, func(db *gorm.DB) error {
		if err := db.First(&r, "row_key = ?", key(tenant, id)).Error; err != nil {
			return err
		}
		return json.Unmarshal(r.Data, &t)
	})
	return t, err
}
func (s *Store) ListTasks(ctx context.Context, tenant string) ([]task.Task, error) {
	var rows []taskRow
	out := []task.Task{}
	err := s.use(ctx, false, func(db *gorm.DB) error {
		if err := db.Where("tenant = ?", tenant).Order("id ASC").Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			var t task.Task
			if err := json.Unmarshal(r.Data, &t); err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	return out, err
}
func (s *Store) UpdateBasis(ctx context.Context, change task.BasisChange) (task.Task, error) {
	var updated task.Task
	err := s.use(ctx, true, func(db *gorm.DB) error {
		var row taskRow
		if e := db.First(&row, "row_key = ?", key(change.Tenant, change.TaskID)).Error; e != nil {
			return e
		}
		if row.Version != change.ExpectedVersion {
			return fault.ErrConflict
		}
		if e := json.Unmarshal(row.Data, &updated); e != nil {
			return e
		}
		updated.Title = change.Title
		updated.Goal = change.Goal
		updated.Criteria = change.Criteria
		updated.Version++
		updated.BasisVersion++
		b, e := json.Marshal(updated)
		if e != nil {
			return e
		}
		r := db.Model(&taskRow{}).Where("row_key = ? AND version = ?", row.Key, change.ExpectedVersion).Updates(map[string]any{"version": updated.Version, "data": b})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fault.ErrConflict
		}
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	return updated, nil
}
