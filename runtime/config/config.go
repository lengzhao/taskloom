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
func BuildFiles(ctx context.Context, basePath, configPath string) (lifecycle.App, *build.Result, error) {
	graph, err := LoadFiles(basePath, configPath)
	if err != nil {
		return nil, nil, err
	}
	app, result, err := build.Build[lifecycle.App](ctx, graph, "server")
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
	if err := checkFlowReferences(graph); err != nil {
		return nil, err
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

// Definition checks concern immutable configuration references only. They do not
// register or implement any task/flow business plugin.
func checkFlowReferences(graph map[string]any) error {
	definitions := map[[2]string]bool{}
	for _, raw := range graph {
		n, _ := raw.(map[string]any)
		if n["use"] != "task/flow" {
			continue
		}
		c, _ := n["config"].(map[string]any)
		id, _ := c["id"].(string)
		version, _ := c["version"].(string)
		key := [2]string{id, version}
		if id == "" || version == "" || definitions[key] {
			return errors.New("invalid or duplicate flow definition reference")
		}
		definitions[key] = true
	}
	for _, raw := range graph {
		n, _ := raw.(map[string]any)
		if n["use"] != "task/catalog" {
			continue
		}
		c, _ := n["config"].(map[string]any)
		ref, _ := c["defaultFlow"].(map[string]any)
		id, _ := ref["id"].(string)
		version, _ := ref["version"].(string)
		if !definitions[[2]string{id, version}] {
			return errors.New("default flow reference is not defined")
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
