package http_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/plugins/api/health"
	transport "github.com/lengzhao/taskloom/plugins/transport/http"
)

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

// Plain http is only for a loopback origin; anything else must be https.
func TestOriginMustBeHTTPSUnlessLoopback(t *testing.T) {
	for origin, ok := range map[string]bool{
		"http://127.0.0.1:8080":      true,
		"http://localhost:8899":      true,
		"http://[::1]:8080":          true,
		"https://tasks.example.com":  true,
		"http://tasks.example.com":   false,
		"https://tasks.example.com/": false,
		"https://user@example.com":   false,
		"tasks.example.com":          false,
	} {
		_, err := transport.New(transport.Config{Listen: "127.0.0.1:0", Origin: origin}, transport.Deps{})
		if (err == nil) != ok {
			t.Errorf("origin %q accepted=%v", origin, err == nil)
		}
	}
}

// Every mounted route gets the shared envelope, without the module asking.
func TestEveryRouteRejectsForeignOriginAndHeaderlessCookieWrites(t *testing.T) {
	h, err := transport.New(transport.Config{Listen: "127.0.0.1:0", Origin: "https://tasks.example.com"}, transport.Deps{Modules: []httpapi.Module{newHealth(t)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, origin, header string
		code                   int
	}{
		{"GET", "", "", 200},
		{"GET", "https://evil.example", "", 403},
		{"GET", "https://tasks.example.com", "", 200},
	} {
		r := httptest.NewRequest(c.method, "/v1/healthz", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.code {
			t.Errorf("%+v got %d", c, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Error("envelope headers missing")
		}
	}
}

type slowModule struct{ entered, release chan struct{} }

func (m slowModule) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "GET", Path: "/slow", Operation: "test.slow", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(m.entered)
		select {
		case <-m.release:
		case <-r.Context().Done():
			http.Error(w, "cancelled", 500)
			return
		}
		_, _ = w.Write([]byte("finished"))
	})}}
}

// Stop drains: a request already running finishes with its context intact
// instead of being cancelled when shutdown begins.
func TestStopLetsInFlightRequestsFinish(t *testing.T) {
	m := slowModule{entered: make(chan struct{}), release: make(chan struct{})}
	h, err := transport.New(transport.Config{Listen: "127.0.0.1:0", APIPrefix: "/v1"}, transport.Deps{Modules: []httpapi.Module{m}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	<-h.Ready()
	body := make(chan string, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + h.Address() + "/v1/slow")
		if err != nil {
			body <- err.Error()
			return
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body <- string(raw)
	}()
	<-m.entered
	stopped := make(chan error, 1)
	go func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		stopped <- h.Stop(stopCtx)
	}()
	// Shutdown has begun once the listener refuses new connections; only then
	// let the in-flight request finish.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", h.Address(), 100*time.Millisecond)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepting during shutdown")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(m.release)
	if got := <-body; got != "finished" {
		t.Fatalf("in-flight request: %q", got)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
