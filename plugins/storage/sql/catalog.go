package sql

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lengzhao/taskloom/cap/catalog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

type tagRow struct {
	Key     string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant  string `gorm:"size:128;index"`
	ID      string `gorm:"size:128"`
	Version int64
	Data    []byte
}

func (s *Store) GetTag(ctx context.Context, tenant, id string) (catalog.Tag, error) {
	var out catalog.Tag
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var row tagRow
		if e := db.First(&row, "row_key = ?", key(tenant, id)).Error; e != nil {
			return e
		}
		return json.Unmarshal(row.Data, &out)
	})
	return out, e
}
func (s *Store) ListTags(ctx context.Context, tenant string) ([]catalog.Tag, error) {
	out := []catalog.Tag{}
	e := s.use(ctx, false, func(db *gorm.DB) error {
		var rows []tagRow
		if e := db.Where("tenant = ?", tenant).Order("id ASC").Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			var t catalog.Tag
			if e := json.Unmarshal(r.Data, &t); e != nil {
				return e
			}
			out = append(out, t)
		}
		return nil
	})
	return out, e
}
func (s *Store) SaveTag(ctx context.Context, v catalog.Tag) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&tagRow{Key: key(v.Tenant, v.ID), Tenant: v.Tenant, ID: v.ID, Version: v.Version, Data: b}).Error
	})
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
func (s *Store) SaveSpace(ctx context.Context, v catalog.Space) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&spaceRow{Key: key(v.Tenant), Version: v.Version, Data: b}).Error
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
func (s *Store) SaveProject(ctx context.Context, v catalog.Project) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&projectRow{Key: key(v.Tenant, v.ID), Tenant: v.Tenant, ID: v.ID, Version: v.Version, Data: b}).Error
	})
}
