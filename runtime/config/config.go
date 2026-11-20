// Package config adapts exactly two files to the upstream merge and PluginKit builder.
// It does not implement its own merge rules or dependency injection container.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	agentconfig "github.com/lengzhao/agentkit/config"
	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	"gopkg.in/yaml.v3"
)

// BuildFiles constructs the reachable server graph. Constructors must not perform IO.
// An empty overlay path means no L1; an explicitly named missing file is an error.
func BuildFiles(ctx context.Context, basePath, configPath string) (lifecycle.Component, *build.Result, error) {
	graph, err := LoadFiles(basePath, configPath)
	if err != nil {
		return nil, nil, err
	}
	app, result, err := build.Build[lifecycle.Component](ctx, graph, "server")
	if err != nil {
		return nil, nil, safeBuildError(err)
	}
	return app, result, nil
}

func LoadFiles(basePath, configPath string) (map[string]any, error) {
	base, err := os.ReadFile(basePath)
	if err != nil {
		return nil, errors.New("cannot read base configuration file")
	}
	if err := checkDocument(base, false); err != nil {
		return nil, err
	}
	var overlay []byte
	if configPath != "" {
		overlay, err = os.ReadFile(configPath)
		if err != nil {
			return nil, errors.New("cannot read config override file")
		}
		if len(bytes.TrimSpace(overlay)) == 0 {
			overlay = nil
		} else {
			if err := checkDocument(overlay, true); err != nil {
				return nil, err
			}
			var m map[string]any
			if yaml.Unmarshal(overlay, &m) == nil && len(m) == 0 {
				overlay = nil
			}
		}
	}
	merged, err := agentconfig.MergeYAML(base, overlay)
	if err != nil {
		return nil, errors.New("configuration merge failed")
	}
	var graph map[string]any
	if err := yaml.Unmarshal(merged, &graph); err != nil {
		return nil, errors.New("invalid merged configuration")
	}
	// Upstream MergeYAML leaves top-level null nodes. Drop them without repairing
	// references: disabling a required instance must fail in PluginKit, never prune it away.
	for id, node := range graph {
		if node == nil {
			delete(graph, id)
		}
	}
	return graph, nil
}

func checkDocument(raw []byte, allowEmpty bool) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var m map[string]any
	if err := decoder.Decode(&m); err != nil {
		if allowEmpty && err == io.EOF {
			return nil
		}
		return errors.New("invalid YAML configuration document")
	}
	var next any
	if err := decoder.Decode(&next); err != io.EOF {
		return errors.New("configuration file must contain exactly one YAML document")
	}
	if !allowEmpty && len(m) == 0 {
		return errors.New("base configuration is empty")
	}
	for _, rawNode := range m {
		if rawNode == nil {
			continue
		}
		node, ok := rawNode.(map[string]any)
		if !ok {
			return errors.New("configuration instance must be an object")
		}
		for key := range node {
			if key != "use" && key != "id" && key != "config" && key != "deps" {
				return errors.New("unsupported configuration instance field")
			}
		}
	}
	return nil
}

// Upstream YAML/constructor errors may contain raw config values. Export only
// structural instance identifiers and the stage; never the underlying message.
func safeBuildError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var e *build.Error
	if errors.As(err, &e) {
		return fmt.Errorf("configuration %s failed (instance=%q)", e.Stage, e.ID)
	}
	return errors.New("configuration build failed")
}
