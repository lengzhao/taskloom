package sql

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/file"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// fileRow registers one stored version. A row is written only after its bytes
// are stored, so it needs no upload status. Uploader is copied out of Data to
// find a viewer's own unreferenced uploads.
type fileRow struct {
	Key      string  `gorm:"primaryKey;size:64;column:row_key"`
	TaskKey  string  `gorm:"size:64;not null;index"`
	Task     taskRow `gorm:"foreignKey:TaskKey;references:Key;constraint:OnDelete:RESTRICT"`
	Uploader string  `gorm:"size:128"`
	Data     []byte
}

// fileRefRow records that some commit references the file (same key): the
// fact that makes an upload visible to every task reader. It is written with
// the commit and never removed, as commits are immutable.
type fileRefRow struct {
	Key string `gorm:"primaryKey;size:64;column:row_key"`
}

// pendingBlobRow records a blob key whose bytes no fileRow owns: an upload
// in progress (reserved before its bytes are written) or bytes left by a
// failed upload or a deleted file. Claimed marks a key handed to cleanup; a
// claimed reservation can no longer be registered.
type pendingBlobRow struct {
	Key       string    `gorm:"primaryKey;size:128;column:row_key"`
	CreatedAt time.Time `gorm:"not null;index"`
	Claimed   bool      `gorm:"not null"`
}

// visible keeps the files a viewer may see: referenced, or its own upload.
func visible(db *gorm.DB, viewer string) *gorm.DB {
	return db.Where("uploader = ? OR EXISTS (SELECT 1 FROM file_ref_rows r WHERE r.row_key = file_rows.row_key)", viewer)
}

func (s *Store) ReserveBlob(ctx context.Context, blobKey string) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Create(&pendingBlobRow{Key: blobKey, CreatedAt: time.Now().UTC()}).Error
	})
}

// RegisterFile consumes the key's unclaimed reservation: without one the bytes
// may already be deleted, so registering them would expose a missing file.
func (s *Store) RegisterFile(ctx context.Context, f file.File) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		res := db.Where("row_key = ? AND claimed = ?", f.ID, false).Delete(&pendingBlobRow{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fault.ErrConflict
		}
		var parent taskRow
		if err := db.Select("row_key").First(&parent, "row_key = ?", key(f.Tenant, f.TaskID)).Error; err != nil {
			return err
		}
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		return db.Omit("Task").Create(&fileRow{Key: key(f.Tenant, f.TaskID, "file", f.ID), TaskKey: parent.Key, Uploader: f.Uploader, Data: b}).Error
	})
}

// DeleteFile removes uploader's own unreferenced file and records its key as
// pending in the same transaction. A reference written by a concurrent commit
// is serialized with it by the write transaction: one of them fails.
func (s *Store) DeleteFile(ctx context.Context, tenant, taskID, id, uploader string) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		k := key(tenant, taskID, "file", id)
		var row fileRow
		if err := visible(db.Omit("Task"), uploader).Select("row_key").First(&row, "row_key = ?", k).Error; err != nil {
			return err
		}
		var refs int64
		if err := db.Model(&fileRefRow{}).Where("row_key = ?", k).Count(&refs).Error; err != nil {
			return err
		}
		if refs > 0 {
			// Visible to every task reader and kept with its commits.
			return fault.ErrConflict
		}
		if err := db.Delete(&fileRow{}, "row_key = ?", k).Error; err != nil {
			return err
		}
		return db.Create(&pendingBlobRow{Key: id, CreatedAt: time.Now().UTC()}).Error
	})
}

func (s *Store) ClaimBlobs(ctx context.Context, before time.Time, limit int) ([]string, error) {
	if limit < 1 {
		return nil, fault.ErrInput
	}
	var keys []string
	err := s.use(ctx, true, func(db *gorm.DB) error {
		if err := db.Model(&pendingBlobRow{}).Where("created_at < ?", before.UTC()).Order("created_at").Limit(limit).Pluck("row_key", &keys).Error; err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		return db.Model(&pendingBlobRow{}).Where("row_key IN ?", keys).Update("claimed", true).Error
	})
	return keys, err
}

func (s *Store) ForgetBlob(ctx context.Context, blobKey string) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Delete(&pendingBlobRow{}, "row_key = ?", blobKey).Error
	})
}

func (s *Store) GetFile(ctx context.Context, tenant, taskID, id, viewer string) (file.File, error) {
	var f file.File
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var row fileRow
		if err := visible(db.Omit("Task"), viewer).First(&row, "row_key = ?", key(tenant, taskID, "file", id)).Error; err != nil {
			return err
		}
		return json.Unmarshal(row.Data, &f)
	})
	return f, err
}
func (s *Store) ListFiles(ctx context.Context, tenant, taskID, viewer string) ([]file.File, error) {
	out := []file.File{}
	err := s.use(ctx, false, func(db *gorm.DB) error {
		var rows []fileRow
		if err := visible(db.Omit("Task").Where("task_key = ?", key(tenant, taskID)), viewer).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			var f file.File
			if err := json.Unmarshal(row.Data, &f); err != nil {
				return err
			}
			out = append(out, f)
		}
		return nil
	})
	sortFiles(out)
	return out, err
}

// referenceFiles reports fault.ErrNotFound unless every id is a file of the
// task that author may see (referenced already, or its own upload), then
// records that they are referenced. Another member's unreferenced upload is
// theirs alone: referencing it would publish it without them.
func referenceFiles(db *gorm.DB, tenant, taskID, author string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = key(tenant, taskID, "file", id)
	}
	var n int64
	if err := visible(db.Model(&fileRow{}), author).Where("row_key IN ?", keys).Count(&n).Error; err != nil {
		return err
	}
	if n != int64(len(ids)) {
		return fault.ErrNotFound
	}
	refs := make([]fileRefRow, len(keys))
	for i, k := range keys {
		refs[i] = fileRefRow{Key: k}
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&refs).Error
}

func sortFiles(fs []file.File) {
	sort.SliceStable(fs, func(i, j int) bool {
		if !fs[i].CreatedAt.Equal(fs[j].CreatedAt) {
			return fs[i].CreatedAt.Before(fs[j].CreatedAt)
		}
		return fs[i].ID < fs[j].ID
	})
}
