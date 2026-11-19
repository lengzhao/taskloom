package sql

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/fault"
	"gorm.io/gorm"
)

type spaceRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	Version int64
	Data    []byte
}
type projectRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant  string `gorm:"size:128;index"`
	ID      string `gorm:"size:128"`
	Version int64
	Data    []byte
}

func (s *Store) GetSpace(ctx context.Context, tenant string) (catalog.Space, error) {
	out := catalog.Space{Tenant: tenant, Name: tenant}
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var row spaceRow
		e := db.First(&row, "row_key = ?", key(tenant)).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		return json.Unmarshal(row.Data, &out)
	})
	return out, e
}
func (s *Store) SaveSpace(ctx context.Context, v catalog.Space, expected int64) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		row := spaceRow{Key: key(v.Tenant), Version: v.Version, Data: b}
		if expected == 0 {
			return db.Create(&row).Error
		}
		r := db.Model(&spaceRow{}).Where("row_key = ? AND version = ?", row.Key, expected).Updates(map[string]any{"version": v.Version, "data": b})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fault.ErrConflict
		}
		return nil
	})
}
func (s *Store) GetProject(ctx context.Context, tenant, id string) (catalog.Project, error) {
	var out catalog.Project
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var row projectRow
		if e := db.First(&row, "row_key = ?", key(tenant, id)).Error; e != nil {
			return e
		}
		return json.Unmarshal(row.Data, &out)
	})
	return out, e
}
func (s *Store) ListProjects(ctx context.Context, tenant string) ([]catalog.Project, error) {
	out := []catalog.Project{}
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var rows []projectRow
		if e := db.Where("tenant = ?", tenant).Order("id ASC").Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			var p catalog.Project
			if e := json.Unmarshal(r.Data, &p); e != nil {
				return e
			}
			out = append(out, p)
		}
		return nil
	})
	return out, e
}
func (s *Store) SaveProject(ctx context.Context, v catalog.Project, expected int64) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		row := projectRow{Key: key(v.Tenant, v.ID), Tenant: v.Tenant, ID: v.ID, Version: v.Version, Data: b}
		if expected == 0 {
			return db.Create(&row).Error
		}
		r := db.Model(&projectRow{}).Where("row_key = ? AND version = ?", row.Key, expected).Updates(map[string]any{"version": v.Version, "data": b})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fault.ErrConflict
		}
		return nil
	})
}
