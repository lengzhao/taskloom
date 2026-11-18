package server_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lengzhao/taskloom/cap/lifecycle"
	"github.com/lengzhao/taskloom/runtime/server"
)

type journal struct {
	sync.Mutex
	items []string
}

func (j *journal) add(s string) { j.Lock(); defer j.Unlock(); j.items = append(j.items, s) }

type resource struct {
	name string
	log  *journal
	fail bool
}

func (r *resource) Open(context.Context) error {
	r.log.add("open:" + r.name)
	if r.fail {
		return errors.New("open failed")
	}
	return nil
}
func (r *resource) Close(context.Context) error { r.log.add("close:" + r.name); return nil }

type component struct {
	ready   chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (c *component) Run(ctx context.Context) error { close(c.ready); <-ctx.Done(); return ctx.Err() }
func (c *component) Stop(context.Context) error    { c.once.Do(func() { close(c.stopped) }); return nil }
func TestCancelStopsComponentsBeforeResources(t *testing.T) {
	log := &journal{}
	c := &component{ready: make(chan struct{}), stopped: make(chan struct{})}
	app := server.New([]lifecycle.Resource{&resource{"sql", log, false}, &resource{"objects", log, false}}, []lifecycle.Component{c}, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	select {
	case <-c.ready:
	case <-time.After(time.Second):
		t.Fatal("component did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown hung")
	}
	select {
	case <-c.stopped:
	default:
		t.Fatal("component not stopped")
	}
	want := []string{"open:sql", "open:objects", "close:objects", "close:sql"}
	if !reflect.DeepEqual(log.items, want) {
		t.Fatalf("got %v", log.items)
	}
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("second Run must fail")
	}
}
func TestOpenFailureClosesEarlierResources(t *testing.T) {
	log := &journal{}
	app := server.New([]lifecycle.Resource{&resource{"sql", log, false}, &resource{"broken", log, true}}, nil, time.Second)
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("expected open failure")
	}
	if want := []string{"open:sql", "open:broken", "close:sql"}; !reflect.DeepEqual(log.items, want) {
		t.Fatalf("got %v", log.items)
	}
}

type failingShutdown struct {
	ready   chan struct{}
	failure error
}

func (c *failingShutdown) Run(ctx context.Context) error {
	close(c.ready)
	<-ctx.Done()
	return c.failure
}
func (*failingShutdown) Stop(context.Context) error { return nil }
func TestShutdownDoesNotDiscardComponentFailure(t *testing.T) {
	failure := errors.New("worker cleanup failed")
	c := &failingShutdown{make(chan struct{}), failure}
	app := server.New(nil, []lifecycle.Component{c}, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	<-c.ready
	cancel()
	if err := <-done; !errors.Is(err, failure) {
		t.Fatalf("lost failure: %v", err)
	}
}
