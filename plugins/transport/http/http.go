// Package http owns an HTTP listener and optional module lifecycle, without task rules.
package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/httpapi"
)

type Config struct {
	Listen       string `json:"listen"`
	APIPrefix    string `json:"apiPrefix"`
	ReadTimeout  string `json:"readTimeout"`
	WriteTimeout string `json:"writeTimeout"`
}

func (c *Config) SetDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.APIPrefix == "" {
		c.APIPrefix = "/v1"
	}
	if c.ReadTimeout == "" {
		c.ReadTimeout = "30s"
	}
	if c.WriteTimeout == "" {
		c.WriteTimeout = "60s"
	}
}
func (c *Config) Validate() error {
	_, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return errors.New("listen must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("invalid listen port")
	}
	if !strings.HasPrefix(c.APIPrefix, "/") || strings.ContainsAny(c.APIPrefix, "?#{} ") || strings.Contains(c.APIPrefix, "..") {
		return errors.New("invalid apiPrefix")
	}
	for _, value := range []string{c.ReadTimeout, c.WriteTimeout} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return errors.New("timeouts must be positive durations")
		}
	}
	return nil
}

type Deps struct {
	Modules []httpapi.Module `json:"modules"`
}

type Server struct {
	cfg              Config
	modules          []httpapi.Module
	mux              *stdhttp.ServeMux
	mu               sync.Mutex
	started, stopped bool
	server           *stdhttp.Server
	cancel           context.CancelFunc
	address          string
	ready            chan struct{}
	stopOnce         sync.Once
	stopDone         chan struct{}
	stopErr          error
}

func init() { pluginkit.Register("transport/http", New) }
func New(c Config, d Deps) (*Server, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	mux := stdhttp.NewServeMux()
	operations := map[string]bool{}
	// ServeMux panics on overlapping patterns. Convert startup errors, never crash a process.
	if err := mount(mux, strings.TrimRight(c.APIPrefix, "/"), d.Modules, operations); err != nil {
		return nil, err
	}
	return &Server{cfg: c, modules: d.Modules, mux: mux, ready: make(chan struct{}), stopDone: make(chan struct{})}, nil
}
func mount(mux *stdhttp.ServeMux, prefix string, modules []httpapi.Module, operations map[string]bool) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("conflicting or invalid HTTP route")
		}
	}()
	for _, m := range modules {
		for _, route := range m.Routes() {
			if route.Operation == "" || operations[route.Operation] {
				return errors.New("missing or duplicate route operation")
			}
			if route.Handler == nil || !strings.HasPrefix(route.Path, "/") || strings.Contains(route.Path, "..") || strings.ContainsAny(route.Path, "?#") {
				return errors.New("invalid route")
			}
			switch route.Method {
			case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
			default:
				return errors.New("invalid route method")
			}
			operations[route.Operation] = true
			mux.Handle(route.Method+" "+prefix+route.Path, route.Handler)
		}
	}
	return nil
}
func (s *Server) Ready() <-chan struct{} { return s.ready }
func (s *Server) Address() string        { s.mu.Lock(); defer s.mu.Unlock(); return s.address }
func (s *Server) isStopped() bool        { s.mu.Lock(); defer s.mu.Unlock(); return s.stopped }

func (s *Server) Run(parent context.Context) (result error) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("HTTP server already started")
	}
	s.started = true
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()
	opened := []httpapi.ModuleLifecycle{}
	defer func() {
		if s.isStopped() {
			<-s.stopDone
		}
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		for i := len(opened) - 1; i >= 0; i-- {
			result = errors.Join(result, opened[i].Close(cleanup))
		}
	}()
	for _, m := range s.modules {
		if ctx.Err() != nil || s.isStopped() {
			return nil
		}
		if managed, ok := m.(httpapi.ModuleLifecycle); ok {
			if err := managed.Open(ctx); err != nil {
				return fmt.Errorf("open HTTP module: %w", err)
			}
			opened = append(opened, managed)
		}
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("HTTP listener could not start")
	}
	defer listener.Close()
	read, _ := time.ParseDuration(s.cfg.ReadTimeout)
	write, _ := time.ParseDuration(s.cfg.WriteTimeout)
	srv := &stdhttp.Server{Handler: s.mux, ReadTimeout: read, WriteTimeout: write, ReadHeaderTimeout: read,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	s.mu.Lock()
	if s.stopped || ctx.Err() != nil {
		s.mu.Unlock()
		return nil
	}
	s.server = srv
	s.address = listener.Addr().String()
	close(s.ready)
	s.mu.Unlock()
	served := make(chan struct{})
	defer close(served)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = s.Stop(shutdown)
		case <-served:
		}
	}()
	err = srv.Serve(listener)
	if errors.Is(err, stdhttp.ErrServerClosed) {
		return nil
	}
	if err != nil {
		return errors.New("HTTP serving failed")
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		srv := s.server
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		go func() {
			defer close(s.stopDone)
			if srv != nil {
				err := srv.Shutdown(ctx)
				if err != nil {
					_ = srv.Close()
				}
				s.stopErr = err
			}
		}()
	})
	select {
	case <-s.stopDone:
		return s.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
