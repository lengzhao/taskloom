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
