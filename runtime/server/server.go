// Package server owns application resources and component cancellation.
package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lengzhao/taskloom/cap/lifecycle"
)

type Server struct {
	resources  []lifecycle.Resource
	components []lifecycle.Component
	timeout    time.Duration
	mu         sync.Mutex
	started    bool
	cancel     context.CancelFunc
	done       chan struct{}
}

func New(resources []lifecycle.Resource, components []lifecycle.Component, timeout time.Duration) *Server {
	return &Server{resources: resources, components: components, timeout: timeout, done: make(chan struct{})}
}

func (s *Server) Run(parent context.Context) (result error) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("server already started")
	}
	s.started = true
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.mu.Unlock()
	opened := 0
	launched := 0
	received := 0
	results := make(chan error, len(s.components))
	defer func() {
		cancel()
		shutdown, stop := context.WithTimeout(context.Background(), s.timeout)
		defer stop()
		// Stop all components before releasing their dependencies.
		for i := launched - 1; i >= 0; i-- {
			result = errors.Join(result, s.components[i].Stop(shutdown))
		}
		for i := received; i < launched; i++ {
			select {
			case err := <-results:
				if err != nil && !errors.Is(err, context.Canceled) {
					result = errors.Join(result, err)
				}
			case <-shutdown.Done():
				result = errors.Join(result, errors.New("component shutdown timed out"))
				i = launched
			}
		}
		for i := opened - 1; i >= 0; i-- {
			result = errors.Join(result, s.resources[i].Close(shutdown))
		}
		close(s.done)
	}()
	for _, r := range s.resources {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := r.Open(ctx); err != nil {
			return fmt.Errorf("open resource: %w", err)
		}
		opened++
	}
	for _, c := range s.components {
		if ctx.Err() != nil {
			break
		}
		launched++
		go func(c lifecycle.Component) { results <- c.Run(ctx) }(c)
	}
	if launched == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-results:
		received++
		if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
			return nil
		}
		if err == nil {
			return errors.New("component exited before shutdown")
		}
		return fmt.Errorf("component failed: %w", err)
	}
}

func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	started := s.started
	s.mu.Unlock()
	if !started {
		return nil
	}
	cancel()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
