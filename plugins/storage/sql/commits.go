package sql

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/commit"
	"gorm.io/gorm"
)

type commitRow struct {
	Key      string `gorm:"primaryKey;size:64;column:row_key"`
	TaskKey  string `gorm:"size:64;index;index:idx_commit_ref,priority:1"`
	Sequence int64
	// Kind and RefID are copied out of Data so verdicts can be found by the result they refer to.
	Kind  string `gorm:"size:32"`
	RefID string `gorm:"size:128;index:idx_commit_ref,priority:2"`
	Data  []byte
}

func (s *Store) Append(ctx context.Context, c commit.Commit) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		var parent taskRow
		if err := db.First(&parent, "row_key = ?", key(c.Tenant, c.TaskID)).Error; err != nil {
			return err
		}
		if c.RefID != "" {
			var ref commitRow
			if err := db.First(&ref, "row_key = ?", key(c.Tenant, c.TaskID, c.RefID)).Error; err != nil {
				return err
			}
		}
		if err := db.Model(&coordination{}).Where("id = ?", 1).UpdateColumn("sequence", gorm.Expr("sequence + 1")).Error; err != nil {
			return err
		}
		var seq coordination
		if err := db.First(&seq, 1).Error; err != nil {
			return err
		}
		c.Sequence = seq.Sequence
		c.CreatedAt = time.Now().UTC()
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		return db.Create(&commitRow{Key: key(c.Tenant, c.TaskID, c.ID), TaskKey: parent.Key, Sequence: c.Sequence, Kind: c.Kind, RefID: c.RefID, Data: data}).Error
	})
}
func (s *Store) Get(ctx context.Context, tenant, taskID, id string) (commit.Commit, error) {
	var c commit.Commit
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var row commitRow
		if err := db.First(&row, "row_key = ?", key(tenant, taskID, id)).Error; err != nil {
			return err
		}
		return json.Unmarshal(row.Data, &c)
	})
	return c, err
}
func (s *Store) List(ctx context.Context, tenant, taskID string, after int64, limit int) ([]commit.Commit, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	out := []commit.Commit{}
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var rows []commitRow
		if err := db.Where("task_key = ? AND sequence > ?", key(tenant, taskID), after).Order("sequence ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			var c commit.Commit
			if err := json.Unmarshal(row.Data, &c); err != nil {
				return err
			}
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

// LatestReview reads the newest verdict commit on a result commit.
func (s *Store) LatestReview(ctx context.Context, tenant, taskID, resultCommitID string) (acceptance.Review, error) {
	var c commit.Commit
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var row commitRow
		if err := db.Where("task_key = ? AND ref_id = ? AND kind IN ?", key(tenant, taskID), resultCommitID, []string{acceptance.KindApproved, acceptance.KindRejected}).Order("sequence DESC").First(&row).Error; err != nil {
			return err
		}
		return json.Unmarshal(row.Data, &c)
	})
	if err != nil {
		return acceptance.Review{}, err
	}
	outcome := acceptance.OutcomeRejected
	if c.Kind == acceptance.KindApproved {
		outcome = acceptance.OutcomeApproved
	}
	return acceptance.Review{CommitID: c.RefID, Reviewer: c.Author, Outcome: outcome, Reason: c.Body, BasisVersion: c.BasisVersion}, nil
}
