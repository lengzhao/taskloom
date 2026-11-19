package sql

import (
	"context"
	"github.com/lengzhao/taskloom/cap/fault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type passwordRow struct {
	Key  string `gorm:"primaryKey;size:64;column:row_key"`
	Hash string `gorm:"size:256;not null"`
}

func (s *Store) PasswordHash(ctx context.Context, tenant, subject string) (string, error) {
	var row passwordRow
	e := s.use(ctx, false, func(db *gorm.DB) error { return db.First(&row, "row_key = ?", key(tenant, subject)).Error })
	return row.Hash, e
}
func (s *Store) SetPasswordHash(ctx context.Context, tenant, subject, hash string) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&passwordRow{Key: key(tenant, subject), Hash: hash}).Error
	})
}
func (s *Store) SingleLoginTenant(ctx context.Context) (string, error) {
	var tenants []string
	e := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Model(&principalRow{}).Where("kind = ?", "human").Distinct("tenant").Limit(2).Pluck("tenant", &tenants).Error
	})
	if e != nil {
		return "", e
	}
	if len(tenants) != 1 {
		return "", fault.ErrInput
	}
	return tenants[0], nil
}
