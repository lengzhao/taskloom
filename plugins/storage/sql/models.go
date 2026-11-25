package sql

import "time"

type coordination struct {
	ID           int `gorm:"primaryKey;autoIncrement:false"`
	Sequence     int64
	Bootstrapped bool
}
type migrationRecord struct {
	ID       string `gorm:"primaryKey;size:128"`
	Checksum string `gorm:"size:64"`
	Complete bool
}
type principalRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant  string `gorm:"size:128;not null"`
	Subject string `gorm:"size:128;not null"`
	Kind    string `gorm:"size:16;not null"`
	OwnerID string `gorm:"size:128"`
}
type credentialRow struct {
	ID           string       `gorm:"primaryKey;size:128"`
	PrincipalKey string       `gorm:"size:64;not null;index"`
	Principal    principalRow `gorm:"foreignKey:PrincipalKey;references:Key;constraint:OnDelete:RESTRICT"`
	Digest       string       `gorm:"size:64;uniqueIndex;not null"`
	ExpiresAt    time.Time
	Revoked      bool
}
type receiptRow struct {
	Key    string `gorm:"primaryKey;size:64;column:row_key"`
	Digest string `gorm:"size:64"`
	Result []byte
}

// receiptRecord is the current receipt model; receiptRow stays frozen as the
// command/001 definition. CreatedAt (added by command/002) bounds retention.
type receiptRecord struct {
	Key       string `gorm:"primaryKey;size:64;column:row_key"`
	Digest    string `gorm:"size:64"`
	Result    []byte
	CreatedAt time.Time `gorm:"index:idx_receipt_created"`
}

func (receiptRecord) TableName() string { return "receipt_rows" }

// An index-only view of an existing table: AutoMigrate adds the named index
// and, with column types matching the frozen row, alters nothing else. The
// audit index is created by DDL instead (see auditTimeIndexDDL): its tenant
// column is TEXT on MySQL, which needs a prefix length.
type commitSequenceIndex struct {
	TaskKey  string `gorm:"size:64;index:idx_commit_task_sequence,priority:1"`
	Sequence int64  `gorm:"index:idx_commit_task_sequence,priority:2"`
}

func (commitSequenceIndex) TableName() string { return "commit_rows" }

type auditRow struct {
	OwnerID, CredentialID                                 string
	ID                                                    string `gorm:"primaryKey;size:128"`
	Tenant, Subject, Operation, Resource, Outcome, Reason string
	CreatedAt                                             time.Time
}
type eventRow struct {
	Sequence       int64  `gorm:"primaryKey;autoIncrement:false;index:idx_event_tenant_sequence,priority:2"`
	Tenant         string `gorm:"size:128;index:idx_event_tenant_sequence,priority:1"`
	Resource, Kind string
	CreatedAt      time.Time
}
