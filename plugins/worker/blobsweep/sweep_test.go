package blobsweep

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type tx struct{}

func (tx) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type pending struct {
	mu      sync.Mutex
	keys    map[string]time.Time
	claimed map[string]bool
}

func (p *pending) ClaimBlobs(_ context.Context, before time.Time, limit int) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for k, at := range p.keys {
		if at.Before(before) && len(out) < limit {
			p.claimed[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}
func (p *pending) ForgetBlob(_ context.Context, k string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.keys, k)
	return nil
}

type blobs struct {
	mu      sync.Mutex
	fail    string
	deleted []string
}

func (b *blobs) Put(context.Context, string, io.Reader) error       { return nil }
func (b *blobs) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (b *blobs) Delete(_ context.Context, k string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if k == b.fail {
		return errors.New("storage down")
	}
	b.deleted = append(b.deleted, k)
	return nil
}

func TestSweepBatchesGraceAndFailures(t *testing.T) {
	now := time.Now()
	p := &pending{keys: map[string]time.Time{}, claimed: map[string]bool{}}
	for _, k := range []string{"a1", "a2", "a3", "a4", "a5"} {
		p.keys[k] = now.Add(-2 * time.Hour)
	}
	p.keys["fresh"] = now.Add(-time.Minute)
	b := &blobs{}
	s, err := New(Config{Grace: "1h", Batch: 2}, Deps{Transactions: tx{}, Pending: p, Blobs: b})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	// All old keys go across batches; one inside the grace period stays.
	if n, err := s.Sweep(context.Background()); err != nil || n != 5 {
		t.Fatal(n, err)
	}
	if _, ok := p.keys["fresh"]; !ok || len(p.keys) != 1 {
		t.Fatal("remaining", p.keys)
	}
	// A failed delete keeps the record for the next sweep.
	p.keys["bad"] = now.Add(-2 * time.Hour)
	b.fail = "bad"
	if _, err := s.Sweep(context.Background()); err == nil {
		t.Fatal("failure not reported")
	}
	if _, ok := p.keys["bad"]; !ok {
		t.Fatal("failed key forgotten")
	}
	b.fail = ""
	if n, err := s.Sweep(context.Background()); err != nil || n != 1 {
		t.Fatal("retry", n, err)
	}
}

func TestRunStopsOnStopAndContext(t *testing.T) {
	p := &pending{keys: map[string]time.Time{}, claimed: map[string]bool{}}
	s, _ := New(Config{Interval: "1h"}, Deps{Transactions: tx{}, Pending: p, Blobs: &blobs{}})
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal("stop before run", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	stop, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(stop); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(stop); err != nil {
		t.Fatal("second stop", err)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("ran twice")
	}

	s2, _ := New(Config{Interval: "1h"}, Deps{Transactions: tx{}, Pending: p, Blobs: &blobs{}})
	ctx, cancel2 := context.WithCancel(context.Background())
	go func() { done <- s2.Run(ctx) }()
	cancel2()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
