package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cliFiles(t *testing.T, listen string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	overlay := filepath.Join(dir, "config.yaml")
	text := "server:\n  use: server\n  deps:\n    resources: []\n    components: [http]\nhttp:\n  use: transport/http\n  config:\n    listen: " + listen + "\n  deps:\n    modules: [health]\nhealth:\n  use: api/health\n"
	if err := os.WriteFile(base, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return base, overlay
}
func TestCheckBuildDoesNotBindSockets(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	base, overlay := cliFiles(t, l.Addr().String())
	var out, errs bytes.Buffer
	code := run(context.Background(), []string{"-base", base, "-config", overlay, "-check"}, &out, &errs)
	if code != 0 || !strings.Contains(out.String(), "3 instances") {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errs)
	}
}
func TestCLIExitsOnContextCancellation(t *testing.T) {
	base, overlay := cliFiles(t, "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var out, errs bytes.Buffer
	if code := run(ctx, []string{"-base", base, "-config", overlay}, &out, &errs); code != 0 {
		t.Fatalf("code=%d error=%s", code, &errs)
	}
}
func TestCLIInvalidConfigAndExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"-base", "missing"}, {"unexpected"}, {"-config", "a", "b"}} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), args, &out, &errs); code == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestHelpDoesNotLoadConfig(t *testing.T) {
	var out, errs bytes.Buffer
	if run(context.Background(), []string{"-help"}, &out, &errs) != 0 {
		t.Fatal("help failed")
	}
}
