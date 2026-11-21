package sql

import (
	"context"
	"encoding/json"

	"github.com/lengzhao/taskloom/cap/task"
	"gorm.io/gorm"
)

type taskRow struct {
	Key    string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant string `gorm:"size:128;index"`
	ID     string `gorm:"size:128"`
	Data   []byte
}

func (s *Store) CreateTask(ctx context.Context, t task.Task) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		seq, err := nextSequence(db)
		if err != nil {
			return err
		}
		t.Sequence = seq
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		return db.Create(&taskRow{Key: key(t.Tenant, t.ID), Tenant: t.Tenant, ID: t.ID, Data: b}).Error
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

// Task writes run under the global write lock, so they are plain updates;
// commands check the client's pinned basis version before writing.
func (s *Store) UpdateBasis(ctx context.Context, change task.BasisChange) (task.Task, error) {
	return s.updateTask(ctx, change.Tenant, change.TaskID, func(t *task.Task) {
		t.Title, t.Goal, t.Criteria = change.Title, change.Goal, change.Criteria
		t.BasisVersion++
	})
}

func (s *Store) AdvanceBasis(ctx context.Context, tenant, taskID string) (task.Task, error) {
	return s.updateTask(ctx, tenant, taskID, func(t *task.Task) { t.BasisVersion++ })
}

func (s *Store) SetProject(ctx context.Context, tenant, taskID, projectID string) (task.Task, error) {
	return s.updateTask(ctx, tenant, taskID, func(t *task.Task) { t.ProjectID = projectID })
}

func (s *Store) updateTask(ctx context.Context, tenant, taskID string, change func(*task.Task)) (task.Task, error) {
	var out task.Task
	e := s.use(ctx, true, func(db *gorm.DB) error {
		var row taskRow
		if e := db.First(&row, "row_key = ?", key(tenant, taskID)).Error; e != nil {
			return e
		}
		if e := json.Unmarshal(row.Data, &out); e != nil {
			return e
		}
		change(&out)
		b, e := json.Marshal(out)
		if e != nil {
			return e
		}
		return db.Model(&taskRow{}).Where("row_key = ?", row.Key).Update("data", b).Error
	})
	return out, e
}

func (s *Store) SetOwner(ctx context.Context, tenant, taskID, owner string) (task.Task, error) {
	return s.updateTask(ctx, tenant, taskID, func(t *task.Task) { t.Owner = owner })
}
