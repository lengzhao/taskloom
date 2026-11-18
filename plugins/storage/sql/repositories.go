package sql

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func key(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func toPrincipal(r principalRow) identity.Principal {
	return identity.Principal{Tenant: r.Tenant, ID: r.Subject, Kind: r.Kind, OwnerID: r.OwnerID}
}
func (s *Store) CreatePrincipal(ctx context.Context, p identity.Principal) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Create(&principalRow{Key: key(p.Tenant, p.ID), Tenant: p.Tenant, Subject: p.ID, Kind: p.Kind, OwnerID: p.OwnerID}).Error
	})
}
func (s *Store) Resolve(ctx context.Context, tenant, id string) (identity.Principal, error) {
	var row principalRow
	err := s.use(ctx, false, func(db *gorm.DB) error { return db.First(&row, "row_key = ?", key(tenant, id)).Error })
	return toPrincipal(row), err
}
func (s *Store) SaveCredential(ctx context.Context, c identity.Credential) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Create(&credentialRow{ID: c.ID, PrincipalKey: key(c.Principal.Tenant, c.Principal.ID), Digest: c.Digest, ExpiresAt: c.ExpiresAt, Revoked: c.Revoked, InstanceID: c.Principal.InstanceID, Incarnation: c.Principal.Incarnation, Purpose: c.Principal.CredentialPurpose}).Error
	})
}
func (s *Store) FindCredential(ctx context.Context, digest string) (identity.Credential, error) {
	var row credentialRow
	err := s.use(ctx, false, func(db *gorm.DB) error { return db.Preload("Principal").First(&row, "digest = ?", digest).Error })
	p := toPrincipal(row.Principal)
	p.InstanceID = row.InstanceID
	p.Incarnation = row.Incarnation
	p.CredentialPurpose = row.Purpose
	return identity.Credential{ID: row.ID, Principal: p, Digest: row.Digest, ExpiresAt: row.ExpiresAt, Revoked: row.Revoked}, err
}
func (s *Store) RevokeCredential(ctx context.Context, tenant, id string) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		sub := db.Model(&principalRow{}).Select("row_key").Where("tenant = ?", tenant)
		r := db.Model(&credentialRow{}).Where("id = ? AND principal_key IN (?)", id, sub).Update("revoked", true)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return unitofwork.ErrNotFound
		}
		return nil
	})
}
func (s *Store) ClaimBootstrap(ctx context.Context) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		r := db.Model(&coordination{}).Where("id = ? AND bootstrapped = ?", 1, false).Update("bootstrapped", true)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return unitofwork.ErrConflict
		}
		return nil
	})
}
func grantKey(g authorization.Grant) string {
	return key(g.Tenant, g.Subject, g.Action, g.Scope.Kind, g.Scope.ID)
}
func (s *Store) HasGrant(ctx context.Context, g authorization.Grant) (bool, error) {
	var count int64
	err := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Model(&grantRow{}).Where("row_key = ?", grantKey(g)).Count(&count).Error
	})
	return count == 1, err
}
func (s *Store) PutGrant(ctx context.Context, g authorization.Grant) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&grantRow{Key: grantKey(g), PrincipalKey: key(g.Tenant, g.Subject), Action: g.Action, Kind: g.Scope.Kind, Resource: g.Scope.ID}).Error
	})
}
func (s *Store) RemoveGrant(ctx context.Context, g authorization.Grant) error {
	return s.use(ctx, true, func(db *gorm.DB) error { return db.Delete(&grantRow{}, "row_key = ?", grantKey(g)).Error })
}
func receiptKey(k record.RequestKey) string { return key(k.Tenant, k.Subject, k.Operation, k.Key) }
func (s *Store) FindReceipt(ctx context.Context, k record.RequestKey) (record.Receipt, error) {
	var r receiptRow
	err := s.use(ctx, false, func(db *gorm.DB) error { return db.First(&r, "row_key = ?", receiptKey(k)).Error })
	return record.Receipt{RequestKey: k, Digest: r.Digest, Result: r.Result}, err
}
func (s *Store) SaveReceipt(ctx context.Context, r record.Receipt) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Create(&receiptRow{Key: receiptKey(r.RequestKey), Digest: r.Digest, Result: r.Result}).Error
	})
}
func (s *Store) AppendAudit(ctx context.Context, a record.Audit) error {
	if a.ID == "" {
		a.ID = randomID()
	}
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Create(&auditRow{ID: a.ID, Tenant: a.Tenant, Subject: a.Subject, Operation: a.Operation, Resource: a.Resource, Outcome: a.Outcome, Reason: a.Reason, OwnerID: a.OwnerID, InstanceID: a.InstanceID, Incarnation: a.Incarnation}).Error
	})
}
func (s *Store) AppendEvent(ctx context.Context, e record.Event) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		if err := db.Model(&coordination{}).Where("id = ?", 1).UpdateColumn("sequence", gorm.Expr("sequence + 1")).Error; err != nil {
			return err
		}
		var c coordination
		if err := db.First(&c, 1).Error; err != nil {
			return err
		}
		if e.ID == "" {
			e.ID = randomID()
		}
		return db.Create(&eventRow{ID: e.ID, Sequence: c.Sequence, Tenant: e.Tenant, Resource: e.Resource, Kind: e.Kind, Payload: e.Payload}).Error
	})
}
func (s *Store) ReadEvents(ctx context.Context, tenant string, after int64, limit int) ([]record.Event, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	var rows []eventRow
	err := s.use(ctx, false, func(db *gorm.DB) error {
		return db.Where("tenant = ? AND sequence > ?", tenant, after).Order("sequence ASC").Limit(limit).Find(&rows).Error
	})
	out := make([]record.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, record.Event{ID: r.ID, Sequence: r.Sequence, Tenant: r.Tenant, Resource: r.Resource, Kind: r.Kind, Payload: r.Payload})
	}
	return out, err
}
func (s *Store) Advance(ctx context.Context, tenant, resource string, expected int64) (int64, error) {
	err := s.use(ctx, true, func(db *gorm.DB) error {
		k := key(tenant, resource)
		if expected == 0 {
			return db.Create(&revisionRow{Key: k, Version: 1}).Error
		}
		r := db.Model(&revisionRow{}).Where("row_key = ? AND version = ?", k, expected).Update("version", expected+1)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return unitofwork.ErrConflict
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return expected + 1, nil
}

func (s *Store) RevokeSubjectCredentials(ctx context.Context, tenant, subject string) error {
	return s.use(ctx, true, func(db *gorm.DB) error {
		return db.Model(&credentialRow{}).Where("principal_key = ?", key(tenant, subject)).Update("revoked", true).Error
	})
}
