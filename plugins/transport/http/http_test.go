package http_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/plugins/api/health"
	transport "github.com/lengzhao/taskloom/plugins/transport/http"
)

type module struct {
	mu            sync.Mutex
	opens, closes int
	fail          bool
	path          string
}

func (m *module) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "GET", Path: m.path, Operation: m.path, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })}}
}
func (m *module) Open(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.opens++
	if m.fail {
		return errors.New("module open failed")
	}
	return nil
}
func (m *module) Close(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closes++
	return nil
}
func TestHealthServeAndGracefulStop(t *testing.T) {
	h, err := transport.New(transport.Config{Listen: "127.0.0.1:0", APIPrefix: "/v1"}, transport.Deps{Modules: []httpapi.Module{newHealth(t)}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	select {
	case <-h.Ready():
	case err := <-done:
		t.Fatalf("startup failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("listener not ready")
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + h.Address() + "/v1/healthz")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"scope":"process"`) {
		t.Fatalf("unexpected health %d %s", resp.StatusCode, raw)
	}
	stopCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := h.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not exit")
	}
	if _, err := client.Get("http://" + h.Address() + "/v1/healthz"); err == nil {
		t.Fatal("listener remains open")
	}
}
func TestRouteCollisionAndInvalidConfig(t *testing.T) {
	if _, err := transport.New(transport.Config{Listen: "127.0.0.1:0"}, transport.Deps{Modules: []httpapi.Module{newHealth(t), newHealth(t)}}); err == nil {
		t.Fatal("duplicate routes accepted")
	}
	if _, err := transport.New(transport.Config{Listen: "invalid"}, transport.Deps{}); err == nil {
		t.Fatal("invalid listen accepted")
	}
}
func TestModuleOpenFailureCleansEarlierModule(t *testing.T) {
	first := &module{path: "/first"}
	broken := &module{path: "/broken", fail: true}
	h, err := transport.New(transport.Config{Listen: "127.0.0.1:0"}, transport.Deps{Modules: []httpapi.Module{first, broken}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Run(context.Background()); err == nil {
		t.Fatal("expected failed module")
	}
	if first.opens != 1 || first.closes != 1 {
		t.Fatalf("lifecycle: %+v", first)
	}
}
func TestStopBeforeRunDoesNotLeaveListener(t *testing.T) {
	h, err := transport.New(transport.Config{Listen: "127.0.0.1:0"}, transport.Deps{Modules: []httpapi.Module{newHealth(t)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.Ready():
		t.Fatal("stopped server became ready")
	default:
	}
}

func newHealth(t *testing.T) httpapi.Module {
	t.Helper()
	m, err := health.New()
	if err != nil {
		t.Fatal(err)
	}
	return m
}
