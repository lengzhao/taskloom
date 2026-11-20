package sql

import (
	"context"

	"github.com/lengzhao/taskloom/cap/plan"
	"gorm.io/gorm"
)

// linkRow is keyed by the child, so a task can never have two parents.
type linkRow struct {
	Key      string `gorm:"primaryKey;size:64;column:row_key"`
	Tenant   string `gorm:"size:128;not null;index:idx_link_parent,priority:1"`
	ParentID string `gorm:"size:128;not null;index:idx_link_parent,priority:2"`
	ChildID  string `gorm:"size:128;not null"`
	Required bool
}

func (r linkRow) link() plan.Link {
	return plan.Link{Tenant: r.Tenant, ParentID: r.ParentID, ChildID: r.ChildID, Required: r.Required}
}

func (s *Store) Parent(ctx context.Context, tenant, childID string) (plan.Link, error) {
	var r linkRow
	err := s.use(ctx, false, func(db *gorm.DB) error { return db.First(&r, "row_key = ?", key(tenant, childID)).Error })
	return r.link(), err
}

func (s *Store) Children(ctx context.Context, tenant, parentID string) ([]plan.Link, error) {
	var rows []linkRow
	out := []plan.Link{}
	err := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Where("tenant = ? AND parent_id = ?", tenant, parentID).Order("child_id ASC").Find(&rows).Error
	})
	for _, r := range rows {
		out = append(out, r.link())
	}
	return out, err
}

// SetParent writes under the global write lock, so a read-then-write is safe.
func (s *Store) SetParent(ctx context.Context, l plan.Link) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		r := linkRow{Key: key(l.Tenant, l.ChildID), Tenant: l.Tenant, ParentID: l.ParentID, ChildID: l.ChildID, Required: l.Required}
		return db.Save(&r).Error
	})
}
