package sql

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Ordered, owned module migrations. Changes require a new ID; never edit an
// applied migration. Failed idempotent DDL is resumed, not marked successful.
type migration struct {
	id, definition string
	models         []any
}

var migrations = []migration{
	{"identity/001", "principal-v1+credential-v2", []any{&principalRow{}, &credentialRow{}}},
	{"command/001", "receipt-v1+audit-v1+event-v1", []any{&receiptRow{}, &auditRow{}, &eventRow{}}},
	{"task/001", "task-facts-and-immutable-commits-v1", []any{&taskRow{}, &commitRow{}, &positionRow{}}},
	{"membership/001", "tenant-members-and-task-participants-v1", []any{&memberRow{}, &participantRow{}}},
	{"workflow/001", "execution-submission-and-state-owned-review-v2", []any{&assignmentRow{}, &executionRow{}, &submissionRow{}, &reviewRow{}, &verdictRow{}, &positionHistoryRow{}}},
	{"question/001", "round-scoped-input-requests-v1", []any{&questionRow{}}},
	{"password/001", "salted-human-password-hash-v1", []any{&passwordRow{}}},
	{"catalog/001", "workspace-and-project-metadata-v1", []any{&spaceRow{}, &projectRow{}}},
}

func migrate(ctx context.Context, db *gorm.DB, driver string) error {
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) (result error) {
		// Server dialect DDL may implicitly commit. A session-level migration lock
		// survives that commit and is released on the same dedicated connection.
		if driver == "postgres" {
			if err := conn.Exec("SELECT pg_advisory_lock(742193581)").Error; err != nil {
				return err
			}
			defer func() { result = releaseMigrationLock(conn, "SELECT pg_advisory_unlock(742193581)", result) }()
		}
		if driver == "mysql" {
			var acquired int
			if err := conn.Raw("SELECT GET_LOCK('taskloom_schema', 10)").Scan(&acquired).Error; err != nil {
				return err
			}
			if acquired != 1 {
				return fmt.Errorf("migration lock unavailable")
			}
			defer func() { result = releaseMigrationLock(conn, "SELECT RELEASE_LOCK('taskloom_schema')", result) }()
		}
		apply := func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&migrationRecord{}, &coordination{}); err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&coordination{ID: 1}).Error; err != nil {
				return err
			}
			var applied []migrationRecord
			if err := tx.Find(&applied).Error; err != nil {
				return err
			}
			known := map[string]string{}
			for _, m := range migrations {
				known[m.id] = migrationChecksum(m)
			}
			for _, a := range applied {
				if sum, ok := known[a.ID]; !ok || sum != a.Checksum {
					return fmt.Errorf("incompatible schema")
				}
			}
			for _, m := range migrations {
				var row migrationRecord
				result := tx.Where("id = ?", m.id).Find(&row)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected == 1 && row.Complete {
					continue
				}
				if result.RowsAffected == 0 {
					if err := tx.Create(&migrationRecord{ID: m.id, Checksum: known[m.id]}).Error; err != nil {
						return err
					}
				}
				if err := tx.AutoMigrate(m.models...); err != nil {
					return err
				}
				if err := tx.Model(&migrationRecord{}).Where("id = ?", m.id).Update("complete", true).Error; err != nil {
					return err
				}
			}
			return nil
		}
		// SQLite's immediate transaction also serializes initial schema creation.
		if driver == "sqlite" {
			return conn.Transaction(apply)
		}
		return apply(conn)
	})
}

func migrationChecksum(m migration) string {
	var b strings.Builder
	b.WriteString(m.definition)
	for _, model := range m.models {
		t := reflect.TypeOf(model).Elem()
		b.WriteString(t.String())
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fmt.Fprintf(&b, "|%s:%s:%s", f.Name, f.Type, f.Tag)
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}
func releaseMigrationLock(db *gorm.DB, query string, prior error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := db.WithContext(ctx).Exec(query).Error
	if prior != nil {
		return prior
	}
	return err
}
