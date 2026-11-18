// Package server registers the process root, not a task workflow engine.
package server

import (
	"errors"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	runtime "github.com/lengzhao/taskloom/runtime/server"
)

type Config struct {
	ShutdownTimeout string `json:"shutdownTimeout"`
}

func (c *Config) SetDefaults() {
	if c.ShutdownTimeout == "" {
		c.ShutdownTimeout = "10s"
	}
}
func (c *Config) Validate() error {
	d, err := time.ParseDuration(c.ShutdownTimeout)
	if err != nil || d <= 0 {
		return errors.New("shutdownTimeout must be a positive duration")
	}
	return nil
}

type Deps struct {
	Resources  []lifecycle.Resource  `json:"resources"`
	Components []lifecycle.Component `json:"components"`
}

func init() { pluginkit.Register("server", New) }
func New(c Config, d Deps) (*runtime.Server, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	timeout, _ := time.ParseDuration(c.ShutdownTimeout)
	return runtime.New(d.Resources, d.Components, timeout), nil
}
