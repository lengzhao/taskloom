package sql

import (
	"context"
	"encoding/json"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type executionRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	TaskKey string `gorm:"size:64;index"`
	// Sequence is a private per-task claim order used only to find the
	// current execution; callers identify executions by ID.
	Sequence int64
	Data     []byte
}
// submissionRow keeps its historical name (and table); it stores delivery.Result
// keyed by the result commit.
type submissionRow struct {
	Key       string `gorm:"primaryKey;size:64;column:row_key"`
	TaskRound string `gorm:"size:64;index"`
	Sequence  int64
	Data      []byte
}

func (s *Store) Current(ctx context.Context, tenant, taskID string) (execution.Execution, error) {
	var out execution.Execution
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var r executionRow
		if e := db.Where("task_key = ?", key(tenant, taskID)).Order("sequence DESC").First(&r).Error; e != nil {
			return e
		}
		return json.Unmarshal(r.Data, &out)
	})
	return out, e
}
func (s *Store) GetExecution(ctx context.Context, tenant, id string) (execution.Execution, error) {
	var out execution.Execution
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var r executionRow
		if e := db.First(&r, "row_key = ?", key(tenant, id)).Error; e != nil {
			return e
		}
		return json.Unmarshal(r.Data, &out)
	})
	return out, e
}
func (s *Store) OpenExecution(ctx context.Context, v execution.Execution) (execution.Execution, error) {
	e := s.use(ctx, true, func(db *gorm.DB) error {
		var max struct{ Value int64 }
		if e := db.Model(&executionRow{}).Select("COALESCE(MAX(sequence),0) AS value").Where("task_key = ?", key(v.Tenant, v.TaskID)).Scan(&max).Error; e != nil {
			return e
		}
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return db.Create(&executionRow{Key: key(v.Tenant, v.ID), TaskKey: key(v.Tenant, v.TaskID), Sequence: max.Value + 1, Data: b}).Error
	})
	return v, e
}
func (s *Store) SaveExecution(ctx context.Context, v execution.Execution) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		r := db.Model(&executionRow{}).Where("row_key = ?", key(v.Tenant, v.ID)).Update("data", b)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fault.ErrConflict
		}
		return nil
	})
}
// CredentialBound scans the task's executions; a task has only a handful, one
// per claim, so no credential column or index is kept for it.
func (s *Store) CredentialBound(ctx context.Context, tenant, taskID, credentialID string) (bool, error) {
	var found bool
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var rows []executionRow
		if e := db.Where("task_key = ?", key(tenant, taskID)).Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			var v execution.Execution
			if e := json.Unmarshal(r.Data, &v); e != nil {
				return e
			}
			if v.CredentialID == credentialID {
				found = true
				return nil
			}
		}
		return nil
	})
	return found, e
}
func (s *Store) CurrentResult(ctx context.Context, tenant, taskID, round string) (delivery.Result, error) {
	var out delivery.Result
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var r submissionRow
		if e := db.Where("task_round = ?", key(tenant, taskID, round)).Order("sequence DESC").First(&r).Error; e != nil {
			return e
		}
		return json.Unmarshal(r.Data, &out)
	})
	return out, e
}
func (s *Store) RegisterResult(ctx context.Context, v delivery.Result) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		var seq coordination
		if e := db.First(&seq, 1).Error; e != nil {
			return e
		}
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return db.Create(&submissionRow{Key: key(v.Tenant, v.CommitID), TaskRound: key(v.Tenant, v.TaskID, v.RoundID), Sequence: seq.Sequence, Data: b}).Error
	})
}
type assignmentRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	Subject string
}

// assignmentRecord is the current assignment model; assignmentRow stays frozen
// as the workflow/001 definition. Assigner is added by workflow/002.
type assignmentRecord struct {
	Key      string `gorm:"primaryKey;size:64;column:row_key"`
	Subject  string
	Assigner string `gorm:"size:128"`
}

func (assignmentRecord) TableName() string { return "assignment_rows" }

func (s *Store) Assignee(ctx context.Context, tenant, taskID string) (string, error) {
	var r assignmentRecord
	e := s.use(ctx, false, func(db *gorm.DB) error { return db.First(&r, "row_key = ?", key(tenant, taskID)).Error })
	return r.Subject, e
}
func (s *Store) AssignSubject(ctx context.Context, tenant, taskID, subject, assigner string) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&assignmentRecord{Key: key(tenant, taskID), Subject: subject, Assigner: assigner}).Error
	})
}

func (s *Store) AssignedBy(ctx context.Context, tenant, taskID string) (string, error) {
	var r assignmentRecord
	e := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Where("row_key = ?", key(tenant, taskID)).Limit(1).Find(&r).Error
	})
	return r.Assigner, e
}

func (s *Store) IsAssigned(ctx context.Context, tenant, taskID, subject string) (bool, error) {
	var count int64
	e := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Model(&assignmentRecord{}).Where("row_key = ? AND subject = ?", key(tenant, taskID), subject).Count(&count).Error
	})
	return count == 1, e
}
