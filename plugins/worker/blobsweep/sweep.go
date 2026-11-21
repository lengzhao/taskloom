// Package blobsweep deletes file bytes that no registered file owns: bytes
// left by an interrupted or failed upload, by a replay, or by a deleted file
// whose immediate cleanup failed. It is a server component; the server owns
// its start and stop, and it holds nothing between sweeps.
package blobsweep

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/blob"
	"github.com/lengzhao/taskloom/cap/file"
	"github.com/lengzhao/taskloom/cap/unitofwork"
)

type Config struct {
	// Interval between sweeps; the first runs when the component starts.
	Interval string `json:"interval"`
	// Grace is how old a pending record must be before it is claimed. An
	// upload still writing when its reservation is claimed fails and is
	// retried by the client, so Grace should exceed the upload transfer limit.
	Grace string `json:"grace"`
	// Batch bounds the keys claimed per transaction.
	Batch int `json:"batch"`
}

func (c *Config) SetDefaults() {
	if c.Interval == "" {
		c.Interval = "10m"
	}
	if c.Grace == "" {
		c.Grace = "1h"
	}
	if c.Batch == 0 {
		c.Batch = 100
	}
}
func (c *Config) Validate() error {
	for _, v := range []string{c.Interval, c.Grace} {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			return errors.New("interval and grace must be positive durations")
		}
	}
	if c.Batch < 1 || c.Batch > 1000 {
		return errors.New("batch must be 1..1000")
	}
	return nil
}

type Deps struct {
	Transactions unitofwork.Manager `json:"transactions"`
	Pending      file.PendingBlobs  `json:"pending"`
	Blobs        blob.Store         `json:"blobs"`
}

type Sweeper struct {
	interval, grace time.Duration
	batch           int
	d               Deps
	now             func() time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func init() { pluginkit.Register("workers/blob-sweep", New) }
func New(c Config, d Deps) (*Sweeper, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if d.Transactions == nil || d.Pending == nil || d.Blobs == nil {
		return nil, errors.New("sweeper capabilities required")
	}
	interval, _ := time.ParseDuration(c.Interval)
	grace, _ := time.ParseDuration(c.Grace)
	return &Sweeper{interval: interval, grace: grace, batch: c.Batch, d: d, now: time.Now}, nil
}

// Run sweeps until ctx ends or Stop is called. A failed sweep is retried at
// the next interval; it never stops the process.
func (s *Sweeper) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	if s.done != nil {
		s.mu.Unlock()
		cancel()
		return errors.New("sweeper already started")
	}
	s.cancel, s.done = cancel, make(chan struct{})
	done := s.done
	s.mu.Unlock()
	defer close(done)
	defer cancel()
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		_, _ = s.Sweep(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (s *Sweeper) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Sweep claims pending keys older than the grace period, deletes their bytes
// and forgets each key whose delete succeeded. It reports how many keys it
// cleaned. A key whose delete failed stays claimed and is claimed again later.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	before := s.now().Add(-s.grace)
	cleaned := 0
	for ctx.Err() == nil {
		var keys []string
		err := s.d.Transactions.Do(ctx, func(tx context.Context) error {
			var e error
			keys, e = s.d.Pending.ClaimBlobs(tx, before, s.batch)
			return e
		})
		if err != nil {
			return cleaned, err
		}
		failed := 0
		for _, k := range keys {
			if err = s.d.Blobs.Delete(ctx, k); err == nil {
				err = s.d.Transactions.Do(ctx, func(tx context.Context) error { return s.d.Pending.ForgetBlob(tx, k) })
			}
			if err != nil {
				failed++
				continue
			}
			cleaned++
		}
		// A batch with failures would be claimed again at once, so wait for
		// the next sweep; a short batch was the last.
		if failed > 0 {
			return cleaned, fmt.Errorf("%d pending blobs not deleted", failed)
		}
		if len(keys) < s.batch {
			return cleaned, nil
		}
	}
	return cleaned, ctx.Err()
}
