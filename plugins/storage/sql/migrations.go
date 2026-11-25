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
	// after runs once the models are migrated (data backfill); its behavior is
	// named by definition, which the checksum covers.
	after func(*gorm.DB) error
}

var migrations = []migration{
	{"identity/001", "principal-v1+credential-v2", []any{&principalRow{}, &credentialRow{}}, nil},
	{"command/001", "receipt-v1+audit-v1+event-v1", []any{&receiptRow{}, &auditRow{}, &eventRow{}}, nil},
	{"task/001", "task-facts-and-immutable-commits-v2", []any{&taskRow{}, &commitRow{}, &positionRow{}}, nil},
	{"membership/001", "tenant-members-and-task-participants-v1", []any{&memberRow{}, &participantRow{}}, nil},
	{"workflow/001", "execution-and-submission-v4", []any{&assignmentRow{}, &executionRow{}, &submissionRow{}}, nil},
	{"question/001", "round-scoped-input-requests-v1", []any{&questionRow{}}, nil},
	{"password/001", "salted-human-password-hash-v1", []any{&passwordRow{}}, nil},
	{"catalog/001", "workspace-and-project-metadata-v1", []any{&spaceRow{}, &projectRow{}}, nil},
	{"plan/001", "single-parent-task-links-v1", []any{&linkRow{}}, nil},
	{"file/001", "immutable-task-file-versions-and-references-v1", []any{&fileRow{}, &fileRefRow{}}, nil},
	{"skill/001", "skill-capabilities-ordered-releases-reviews-current-vectors-v2", []any{&skillCapabilityRow{}, &skillReleaseRow{}, &skillReviewRow{}, &skillVectorRow{}}, nil},
	// Existing receipts start their retention at upgrade time rather than expiring at once.
	// The backfill uses the same clock as GORM's own timestamps (local time), so
	// SQLite's textual comparison in pruning sees one offset.
	{"command/002", "receipt-created-at-backfilled-now+audit-tenant-time-index-ddl+commit-task-sequence-index-v2", []any{&receiptRecord{}, &commitSequenceIndex{}}, func(db *gorm.DB) error {
		if err := db.Model(&receiptRecord{}).Where("created_at IS NULL").Update("created_at", time.Now()).Error; err != nil {
			return err
		}
		return auditTimeIndexDDL(db)
	}},
	{"workflow/002", "assignment-assigner-v1", []any{&assignmentRecord{}}, nil},
	{"catalog/002", "tenant-tag-vocabulary-v1", []any{&tagRow{}}, nil},
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
			if err := tx.AutoMigrate(&migrationRecord{}, &coordination{}, &eventFloor{}); err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&coordination{ID: 1}).Error; err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&eventFloor{ID: 1}).Error; err != nil {
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
				if m.after != nil {
					if err := m.after(tx); err != nil {
						return err
					}
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

// auditTimeIndexDDL adds (tenant, created_at) to audit_rows. MySQL stores the
// unsized tenant column as LONGTEXT, which can only be indexed by prefix.
func auditTimeIndexDDL(db *gorm.DB) error {
	const name = "idx_audit_tenant_created"
	if db.Migrator().HasIndex(&auditRow{}, name) {
		return nil
	}
	ddl := "CREATE INDEX " + name + " ON audit_rows (tenant, created_at)"
	if db.Dialector.Name() == "mysql" {
		ddl = "CREATE INDEX " + name + " ON audit_rows (tenant(128), created_at)"
	}
	return db.Exec(ddl).Error
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
