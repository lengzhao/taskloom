package sql

import (
	"context"
	"encoding/json"
	"github.com/lengzhao/taskloom/cap/fault"

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
		b, err := encodeTask(t)
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
		return decodeTask(r.Data, &t)
	})
	return t, err
}
func (s *Store) ListTasks(ctx context.Context, tenant string) ([]task.Task, error) {
	return s.listTasks(ctx, tenant, "", 0)
}
func (s *Store) ListTasksAfter(ctx context.Context, tenant, after string, limit int) ([]task.Task, error) {
	if limit < 1 {
		return nil, fault.ErrInput
	}
	return s.listTasks(ctx, tenant, after, limit)
}
func (s *Store) listTasks(ctx context.Context, tenant, after string, limit int) ([]task.Task, error) {
	var rows []taskRow
	out := []task.Task{}
	err := s.use(ctx, false, func(db *gorm.DB) error {
		q := db.Where("tenant = ? AND id > ?", tenant, after).Order("id ASC")
		if limit > 0 {
			q = q.Limit(limit)
		}
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			var t task.Task
			if err := decodeTask(r.Data, &t); err != nil {
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
		if e := decodeTask(row.Data, &out); e != nil {
			return e
		}
		change(&out)
		b, e := encodeTask(out)
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

// storedTask keeps the fields a caller never reads, the tenant and the
// creation sequence, in the stored JSON; task.Task leaves them out of its own.
type storedTask struct {
	task.Task
	Tenant   string
	Sequence int64
}

func encodeTask(t task.Task) ([]byte, error) {
	return json.Marshal(storedTask{Task: t, Tenant: t.Tenant, Sequence: t.Sequence})
}

func decodeTask(data []byte, t *task.Task) error {
	var v storedTask
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*t = v.Task
	t.Tenant, t.Sequence = v.Tenant, v.Sequence
	return nil
}
