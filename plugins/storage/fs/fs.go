// Package fs stores file bytes in a local directory. It is the single-node
// blob store; an S3 provider implements the same cap/blob contract.
package fs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
)

type Config struct {
	// Dir holds the objects; relative paths resolve against the working directory.
	Dir string `json:"dir"`
}

func (c *Config) SetDefaults() {
	if c.Dir == "" {
		c.Dir = "taskloom-files"
	}
}
func (c *Config) Validate() error {
	if c.Dir == "" {
		return errors.New("blob directory required")
	}
	return nil
}

type Store struct {
	dir  string
	mu   sync.RWMutex
	open bool
}

func init() { pluginkit.Register("storage/fs", New) }
func New(c Config) (*Store, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Store{dir: filepath.Clean(c.Dir)}, nil
}

// Open creates the directory and removes temporary files an interrupted Put
// left behind (no Put of this store runs before Open); Close only ends use.
// One process owns a directory, like one store owns a SQLite file.
func (s *Store) Open(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open {
		return errors.New("blob store already open")
	}
	tmp := filepath.Join(s.dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return fault.ErrUnavailable
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return fault.ErrUnavailable
	}
	for _, e := range entries {
		if err = os.RemoveAll(filepath.Join(tmp, e.Name())); err != nil {
			return fault.ErrUnavailable
		}
	}
	s.open = true
	return nil
}
func (s *Store) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open = false
	return nil
}

// path maps a key to <dir>/<first two chars>/<key>. Keys are generated
// lower-hex IDs; anything else is rejected so a key never escapes the directory.
func (s *Store) path(key string) (string, error) {
	if len(key) < 8 || len(key) > 128 {
		return "", fault.ErrInput
	}
	for _, r := range key {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "", fault.ErrInput
		}
	}
	return filepath.Join(s.dir, key[:2], key), nil
}

func (s *Store) ready() error {
	if !s.open {
		return fault.ErrUnavailable
	}
	return nil
}

// Put writes to a temporary file, syncs it, then links it into place so a
// reader never sees a partial object and an existing key is never replaced.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ready(); err != nil {
		return err
	}
	target, err := s.path(key)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "tmp"), "put-*")
	if err != nil {
		return fault.ErrUnavailable
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, contextReader{ctx, r})
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A reader-side rejection (such as a size limit) keeps its category.
		for _, known := range []error{fault.ErrInput, fault.ErrTooLarge} {
			if errors.Is(err, known) {
				return known
			}
		}
		return fault.ErrUnavailable
	}
	parent := filepath.Dir(target)
	if err = os.MkdirAll(parent, 0o700); err != nil {
		return fault.ErrUnavailable
	}
	if err = os.Link(tmp.Name(), target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fault.ErrConflict
		}
		return fault.ErrUnavailable
	}
	// The caller registers the object in SQL after Put returns, so the new
	// directory entries must be durable first: the object's own directory and
	// the root, which holds that directory when it was just created.
	for _, dir := range []string{parent, s.dir} {
		if err = syncDir(dir); err != nil {
			_ = os.Remove(target)
			return fault.ErrUnavailable
		}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// Get opens a stored object for reading.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ready(); err != nil {
		return nil, err
	}
	target, err := s.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fault.ErrNotFound
	}
	if err != nil {
		return nil, fault.ErrUnavailable
	}
	return f, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ready(); err != nil {
		return err
	}
	target, err := s.path(key)
	if err != nil {
		return err
	}
	if err = os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fault.ErrUnavailable
	}
	return nil
}

// contextReader stops a long copy once the request is cancelled.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
