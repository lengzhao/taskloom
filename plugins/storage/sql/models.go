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
	InstanceID   string `gorm:"size:128"`
	Incarnation  string `gorm:"size:128"`
	Purpose      string `gorm:"size:16"`
}
type grantRow struct {
	Key          string       `gorm:"primaryKey;size:64;column:row_key"`
	PrincipalKey string       `gorm:"size:64;not null;index"`
	Principal    principalRow `gorm:"foreignKey:PrincipalKey;references:Key;constraint:OnDelete:RESTRICT"`
	Action       string       `gorm:"size:128"`
	Kind         string       `gorm:"size:32"`
	Resource     string       `gorm:"size:128"`
}
type receiptRow struct {
	Key    string `gorm:"primaryKey;size:64;column:row_key"`
	Digest string `gorm:"size:64"`
	Result []byte
}
type auditRow struct {
	OwnerID, InstanceID, Incarnation                      string
	ID                                                    string `gorm:"primaryKey;size:128"`
	Tenant, Subject, Operation, Resource, Outcome, Reason string
	CreatedAt                                             time.Time
}
type eventRow struct {
	ID             string `gorm:"primaryKey;size:128"`
	Sequence       int64  `gorm:"uniqueIndex"`
	Tenant         string `gorm:"size:128;index:idx_event_tenant_sequence,priority:1"`
	Resource, Kind string
	Payload        []byte
	CreatedAt      time.Time
}
type revisionRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	Version int64
}
