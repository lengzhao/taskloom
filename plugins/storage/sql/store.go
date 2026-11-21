// Package sql implements narrow repository capabilities using GORM internally.
package sql

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Transaction misuse errors belong to this store's Do implementation; no
// consumer outside it distinguishes them.
var (
	ErrRollback    = errors.New("transaction is rollback-only")
	ErrTransaction = errors.New("active owning transaction required")
)

type Config struct {
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
}

func (c *Config) SetDefaults() {
	if c.Driver == "" {
		c.Driver = "sqlite"
	}
	if c.DSN == "" && c.Driver == "sqlite" {
		c.DSN = "taskloom.db"
	}
}
func (c *Config) Validate() error {
	switch c.Driver {
	case "sqlite", "mysql", "postgres":
	default:
		return errors.New("unsupported SQL driver")
	}
	if c.DSN == "" {
		return errors.New("SQL DSN required")
	}
	return nil
}

type Store struct {
	config Config
	mu     sync.RWMutex
	db     *gorm.DB
}

func init() { pluginkit.Register("storage/sql", New) }
func New(c Config) (*Store, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Store{config: c}, nil
}
func (s *Store) Open(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return errors.New("storage already open")
	}
	var dialect gorm.Dialector
	switch s.config.Driver {
	case "sqlite":
		// This driver supports per-connection pragmas. Force foreign keys and an
		// immediate write transaction; caller DSNs cannot disable these invariants.
		dsn := s.config.DSN
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		_, query, _ := strings.Cut(dsn, "?")
		params, err := url.ParseQuery(query)
		if err != nil {
			return errors.New("invalid SQLite DSN query")
		}
		for _, key := range []string{"_foreign_keys", "_fk", "_txlock", "_busy_timeout", "_timeout"} {
			if params.Has(key) {
				return errors.New("SQLite DSN overrides a managed safety setting")
			}
		}
		dialect = sqlite.Open(dsn + sep + "_foreign_keys=on&_busy_timeout=5000&_txlock=immediate")
	case "mysql":
		dialect = mysql.Open(s.config.DSN)
	case "postgres":
		dialect = postgres.Open(s.config.DSN)
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true, DisableAutomaticPing: true})
	if err != nil {
		return fault.ErrUnavailable
	}
	raw, err := db.DB()
	if err != nil {
		return fault.ErrUnavailable
	}
	// A single connection per store keeps SQLite pragmas predictable. Multiple
	// processes/stores synchronize in the database, never by this mutex.
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	if err = raw.PingContext(ctx); err == nil {
		err = migrate(ctx, db, s.config.Driver)
	}
	if err != nil {
		_ = raw.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fault.ErrUnavailable
	}
	s.db = db
	return nil
}
func (s *Store) Close(ctx context.Context) error {
	for !s.mu.TryLock() {
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	raw, err := s.db.DB()
	s.db = nil
	if err != nil {
		return fault.ErrUnavailable
	}
	if raw.Close() != nil {
		return fault.ErrUnavailable
	}
	return nil
}

type transactionKey struct{}
type transaction struct {
	owner          *Store
	db             *gorm.DB
	active, failed bool
}

func (s *Store) Do(ctx context.Context, fn func(context.Context) error) error {
	if t, ok := ctx.Value(transactionKey{}).(*transaction); ok {
		if !t.active || t.owner != s {
			t.failed = true
			return ErrTransaction
		}
		defer func() {
			if p := recover(); p != nil {
				t.failed = true
				panic(p)
			}
		}()
		err := fn(ctx)
		if err != nil {
			t.failed = true
		}
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return fault.ErrUnavailable
	}
	var callbackErr error
	err := s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		// UPDATE obtains an exclusive write lock until COMMIT (all three dialects).
		if err := db.Model(&coordination{}).Where("id = ?", 1).UpdateColumn("sequence", gorm.Expr("sequence + 1")).Error; err != nil {
			return err
		}
		t := &transaction{owner: s, db: db, active: true}
		defer func() { t.active = false }()
		callbackErr = fn(context.WithValue(ctx, transactionKey{}, t))
		if callbackErr != nil {
			return callbackErr
		}
		if t.failed {
			callbackErr = ErrRollback
			return callbackErr
		}
		return nil
	})
	if callbackErr != nil {
		return callbackErr
	}
	return normalize(ctx, err)
}
func (s *Store) use(ctx context.Context, write bool, fn func(*gorm.DB) error) error {
	if t, ok := ctx.Value(transactionKey{}).(*transaction); ok {
		if !t.active || t.owner != s {
			t.failed = true
			return ErrTransaction
		}
		err := normalize(ctx, fn(t.db.WithContext(ctx)))
		if err != nil && write {
			t.failed = true
		}
		return err
	}
	if write {
		return ErrTransaction
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return fault.ErrUnavailable
	}
	return normalize(ctx, fn(s.db.WithContext(ctx)))
}
func normalize(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fault.ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return fault.ErrConflict
	}
	for _, known := range []error{fault.ErrNotFound, fault.ErrConflict, ErrTransaction, ErrRollback} {
		if errors.Is(err, known) {
			return known
		}
	}
	return fault.ErrUnavailable // Never expose DSNs, values or SQL in errors.
}

// nextSequence advances the global counter that orders tasks, commits and
// events. Commands hold the coordination lock, so the value is the commit order.
func nextSequence(db *gorm.DB) (int64, error) {
	if err := db.Model(&coordination{}).Where("id = ?", 1).UpdateColumn("sequence", gorm.Expr("sequence + 1")).Error; err != nil {
		return 0, err
	}
	var c coordination
	if err := db.First(&c, 1).Error; err != nil {
		return 0, err
	}
	return c.Sequence, nil
}
