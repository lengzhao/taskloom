package sql

import (
	"context"
	"encoding/json"
	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type executionRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	TaskKey string `gorm:"size:64;index"`
	// Sequence is a private per-task claim order used only to find the
	// current execution; callers identify executions by ID.
	Sequence int64
	Data     []byte
}
type submissionRow struct {
	Key       string `gorm:"primaryKey;size:64;column:row_key"`
	TaskRound string `gorm:"size:64;index"`
	Sequence  int64
	Data      []byte
}
type reviewRow struct {
	Key  string `gorm:"primaryKey;size:64;column:row_key"`
	Data []byte
}
type verdictRow struct {
	ID   string `gorm:"primaryKey;size:128"`
	Data []byte
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
func (s *Store) CurrentSubmission(ctx context.Context, tenant, taskID, round string) (delivery.Submission, error) {
	var out delivery.Submission
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var r submissionRow
		if e := db.Where("task_round = ?", key(tenant, taskID, round)).Order("sequence DESC").First(&r).Error; e != nil {
			return e
		}
		return json.Unmarshal(r.Data, &out)
	})
	if e == nil && out.Withdrawn {
		return out, fault.ErrNotFound
	}
	return out, e
}
func (s *Store) GetSubmission(ctx context.Context, tenant, id string) (delivery.Submission, error) {
	var out delivery.Submission
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var r submissionRow
		if e := db.First(&r, "row_key = ?", key(tenant, id)).Error; e != nil {
			return e
		}
		return json.Unmarshal(r.Data, &out)
	})
	return out, e
}
func (s *Store) RegisterSubmission(ctx context.Context, v delivery.Submission) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		var seq coordination
		if e := db.First(&seq, 1).Error; e != nil {
			return e
		}
		v.CreatedAt = time.Now().UTC()
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return db.Create(&submissionRow{Key: key(v.Tenant, v.ID), TaskRound: key(v.Tenant, v.TaskID, v.RoundID), Sequence: seq.Sequence, Data: b}).Error
	})
}
func (s *Store) WithdrawSubmission(ctx context.Context, tenant, id string) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		var r submissionRow
		if e := db.First(&r, "row_key = ?", key(tenant, id)).Error; e != nil {
			return e
		}
		var v delivery.Submission
		if e := json.Unmarshal(r.Data, &v); e != nil {
			return e
		}
		v.Withdrawn = true
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return db.Model(&submissionRow{}).Where("row_key = ?", r.Key).Update("data", b).Error
	})
}
func reviewKey(v acceptance.Review) string {
	return key(v.Tenant, v.TaskID, v.RoundID, v.SubmissionID)
}
func (s *Store) GetReview(ctx context.Context, tenant, taskID, round, submission string) (acceptance.Review, error) {
	var v acceptance.Review
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var r reviewRow
		if e := db.First(&r, "row_key = ?", key(tenant, taskID, round, submission)).Error; e != nil {
			return e
		}
		return json.Unmarshal(r.Data, &v)
	})
	return v, e
}

// SaveReview keeps every verdict as history and the latest per submission as current.
func (s *Store) SaveReview(ctx context.Context, v acceptance.Review) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if e = db.Create(&verdictRow{ID: randomID(), Data: b}).Error; e != nil {
			return e
		}
		return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&reviewRow{Key: reviewKey(v), Data: b}).Error
	})
}

type assignmentRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	Subject string
}

func (s *Store) Assignee(ctx context.Context, tenant, taskID string) (string, error) {
	var r assignmentRow
	e := s.use(ctx, false, func(db *gorm.DB) error { return db.First(&r, "row_key = ?", key(tenant, taskID)).Error })
	return r.Subject, e
}
func (s *Store) AssignSubject(ctx context.Context, tenant, taskID, subject string) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&assignmentRow{Key: key(tenant, taskID), Subject: subject}).Error
	})
}

func (s *Store) IsAssigned(ctx context.Context, tenant, taskID, subject string) (bool, error) {
	var count int64
	e := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Model(&assignmentRow{}).Where("row_key = ? AND subject = ?", key(tenant, taskID), subject).Count(&count).Error
	})
	return count == 1, e
}
