// Command taskloom-agent is the stock AgentKit runner with the Taskloom
// platform plugin (platform/taskloom) linked in. Configuration, plugins and
// lifecycle are AgentKit's own; see deploy/agent for the overlay.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lengzhao/agentkit"
	"github.com/lengzhao/agentkit/config"
	_ "github.com/lengzhao/agentkit/plugins"
	"github.com/lengzhao/pluginkit/build"

	_ "github.com/lengzhao/taskloom/harness/agentkit"
)

func main() {
	basePath := flag.String("base", config.DefaultBasePath, "AgentKit L0 base config YAML path")
	overlayPath := flag.String("config", config.DefaultOverlayPath, "L1 override YAML path(s), comma-separated; later files win")
	logLevel := flag.String("log-level", envOr("AGENTKIT_LOG_LEVEL", "info"), "log level: debug, info, warn, error")
	check := flag.Bool("check", false, "build the runner graph and exit")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(*logLevel)})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	doc, err := config.LoadDocument(*basePath, config.SplitOverlayPaths(*overlayPath)...)
	if err != nil {
		fatal("load config", err)
	}
	runner, result, err := build.Build[agentkit.Runner](ctx, doc.ToGraph(), doc.RootID)
	if err != nil {
		fatal("build runner", err)
	}
	if *check {
		stopRunner(runner)
		fmt.Println("ok")
		return
	}
	slog.Info("runner starting")
	runErr := runner.Run(ctx, result)
	failed := runErr != nil && ctx.Err() == nil
	stopRunner(runner)
	slog.Info("agent stopped")
	if failed {
		fatal("run agent", runErr)
	}
}

func stopRunner(r agentkit.Runner) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		slog.Warn("runner stop", "err", err)
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "error: %s: %v\n", msg, err)
	os.Exit(1)
}
