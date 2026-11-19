// Command taskloom-mcp is a thin MCP (stdio) adapter over the shared Taskloom
// HTTP API. It holds no business rules: authorization, versions, generations
// and idempotency are all enforced by the server for the agent's own
// business credential. It never handles runtime credentials or stop proofs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func run(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) int {
	flags := flag.NewFlagSet("taskloom-mcp", flag.ContinueOnError)
	flags.SetOutput(errout)
	url := flags.String("url", env("TASKLOOM_URL", "http://localhost:8899/v1"), "Taskloom API base URL including /v1 (TASKLOOM_URL)")
	credentialFile := flags.String("credential-file", env("TASKLOOM_CREDENTIAL_FILE", ""), "agent business credential JSON file (TASKLOOM_CREDENTIAL_FILE)")
	renew := flags.Duration("renew", 20*time.Second, "automatic lease renewal interval while a run is open (0 disables)")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	token := os.Getenv("TASKLOOM_TOKEN")
	if token == "" && *credentialFile != "" {
		raw, err := os.ReadFile(*credentialFile)
		if err != nil {
			fmt.Fprintln(errout, "read credential file:", err)
			return 2
		}
		var c struct{ Secret string }
		if json.Unmarshal(raw, &c) != nil || c.Secret == "" {
			fmt.Fprintln(errout, "credential file has no Secret")
			return 2
		}
		token = c.Secret
	}
	if token == "" {
		fmt.Fprintln(errout, "set TASKLOOM_TOKEN or TASKLOOM_CREDENTIAL_FILE to the agent's business credential")
		return 2
	}
	s := newServer(newClient(strings.TrimRight(*url, "/"), token), *renew, errout)
	defer s.Close()
	if err := s.Serve(ctx, in, out); err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}
