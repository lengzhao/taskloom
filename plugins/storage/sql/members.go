package sql

import (
	"context"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type memberRow struct {
	Key                   string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant, Subject, Role string
	Active                bool
}
type participantRow struct {
	Key                     string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant, TaskID, Subject string
}

func (s *Store) GetMember(ctx context.Context, tenant, subject string) (az.Member, error) {
	var row memberRow
	e := s.use(ctx, false, func(db *gorm.DB) error { return db.First(&row, "row_key = ?", key(tenant, subject)).Error })
	return az.Member{Tenant: row.Tenant, Subject: row.Subject, Role: row.Role, Active: row.Active}, e
}
func (s *Store) SetMember(ctx context.Context, m az.Member) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&memberRow{Key: key(m.Tenant, m.Subject), Tenant: m.Tenant, Subject: m.Subject, Role: m.Role, Active: m.Active}).Error
	})
}
func (s *Store) HasParticipant(ctx context.Context, p az.Participant) (bool, error) {
	var count int64
	e := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Model(&participantRow{}).Where("row_key = ?", key(p.Tenant, p.TaskID, p.Subject)).Count(&count).Error
	})
	return count == 1, e
}
func (s *Store) SetParticipant(ctx context.Context, p az.Participant, present bool) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		row := participantRow{Key: key(p.Tenant, p.TaskID, p.Subject), Tenant: p.Tenant, TaskID: p.TaskID, Subject: p.Subject}
		if !present {
			return db.Delete(&participantRow{}, "row_key = ?", row.Key).Error
		}
		return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	})
}

func (s *Store) ListActiveMembers(ctx context.Context, tenant string) ([]az.MemberOption, error) {
	out := []az.MemberOption{}
	e := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Table("member_rows AS m").Select("m.subject, p.kind, p.owner_id").
			Joins("JOIN principal_rows AS p ON p.tenant = m.tenant AND p.subject = m.subject").
			Where("m.tenant = ? AND m.active = ? AND m.role IN ? AND p.kind IN ?", tenant, true, []string{"member", "admin"}, []string{"human", "agent"}).
			Order("p.kind, m.subject").Scan(&out).Error
	})
	return out, e
}

func (s *Store) ListMembers(ctx context.Context, tenant string) ([]az.DirectoryMember, error) {
	out := []az.DirectoryMember{}
	e := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Table("member_rows AS m").Select("m.subject, p.kind, p.owner_id, m.role, m.active").
			Joins("JOIN principal_rows AS p ON p.tenant = m.tenant AND p.subject = m.subject").
			Where("m.tenant = ?", tenant).Order("p.kind, m.subject").Scan(&out).Error
	})
	return out, e
}
