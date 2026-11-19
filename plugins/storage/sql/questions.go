package sql

import (
	"context"
	"encoding/json"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/question"
	"gorm.io/gorm"
)

// questionRow keeps the queried fields as columns; Data holds the full record.
type questionRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant  string `gorm:"size:128;index:idx_question_round,priority:1"`
	TaskID  string `gorm:"size:128;index:idx_question_round,priority:2"`
	RoundID string `gorm:"size:64;index:idx_question_round,priority:3"`
	Status  string `gorm:"size:16;index:idx_question_round,priority:4"`
	Seq     int64  `gorm:"autoIncrement:false"`
	Data    []byte
}

func questionKey(tenant, taskID, id string) string { return key(tenant, taskID, id) }

func (s *Store) CreateQuestion(ctx context.Context, v question.Question) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return db.Create(&questionRow{Key: questionKey(v.Tenant, v.TaskID, v.ID), Tenant: v.Tenant, TaskID: v.TaskID, RoundID: v.RoundID, Status: v.Status, Seq: v.CreatedAt.UnixNano(), Data: b}).Error
	})
}

func (s *Store) GetQuestion(ctx context.Context, tenant, taskID, id string) (question.Question, error) {
	var v question.Question
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var r questionRow
		if e := db.First(&r, "row_key = ?", questionKey(tenant, taskID, id)).Error; e != nil {
			return e
		}
		return json.Unmarshal(r.Data, &v)
	})
	return v, e
}

func (s *Store) OpenQuestions(ctx context.Context, tenant, taskID, roundID string) ([]question.Question, error) {
	out := []question.Question{}
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var rows []questionRow
		if e := db.Where("tenant = ? AND task_id = ? AND round_id = ? AND status = ?", tenant, taskID, roundID, question.StatusOpen).Order("seq").Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			var v question.Question
			if e := json.Unmarshal(r.Data, &v); e != nil {
				return e
			}
			out = append(out, v)
		}
		return nil
	})
	return out, e
}

// AnswerQuestion only moves an open question, so two concurrent answers cannot
// both win and an answered question is never reopened.
func (s *Store) AnswerQuestion(ctx context.Context, v question.Question) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		r := db.Model(&questionRow{}).Where("row_key = ? AND status = ?", questionKey(v.Tenant, v.TaskID, v.ID), question.StatusOpen).Updates(map[string]any{"status": v.Status, "data": b})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fault.ErrConflict
		}
		return nil
	})
}
