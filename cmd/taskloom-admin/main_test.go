package main

import (
	"io"

	"bytes"
	"context"
	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"github.com/lengzhao/taskloom/plugins/security"
	"github.com/lengzhao/taskloom/runtime/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bootstrap and recovery set a password and write nothing: no administrator
// credential ever lands on disk. Bootstrap happens once; recovery replaces
// the password.
func TestBootstrapOnceAndRecoveryReplacePasswordWithoutFiles(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(overlay, []byte(dbOverlay(dir)), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-base", "../../base.yaml", "-config", overlay, "-mode", "bootstrap", "-password-stdin", "-tenant", "t", "-subject", "admin", "-operator", "operator", "-reason", "initial"}
	var out, errs bytes.Buffer
	if code := run(context.Background(), args, password(), &out, &errs); code != 0 {
		t.Fatalf("code=%d %s", code, &errs)
	}
	if code := run(context.Background(), args, password(), &out, &errs); code == 0 {
		t.Fatal("repeated bootstrap")
	}
	if code := run(context.Background(), append(args, "-credential-file", filepath.Join(dir, "admin.json")), password(), &out, &errs); code != 2 {
		t.Fatal("bootstrap accepted a credential file", code)
	}
	args[5] = "recover"
	if code := run(context.Background(), args, strings.NewReader("RecoveredPassword123!\n"), &out, &errs); code != 0 {
		t.Fatalf("recovery: %d %s", code, &errs)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "config.yaml" && !strings.HasPrefix(e.Name(), "db") {
			t.Fatal("administrator modes wrote a file", e.Name())
		}
	}
	sec, done := open(t, overlay)
	defer done()
	if _, e := sec.Login(context.Background(), capsecurity.LoginInput{Tenant: "t", Username: "admin", Password: "AdminPassword123!"}); e == nil {
		t.Fatal("old password still valid after recovery")
	}
	if _, e := sec.Login(context.Background(), capsecurity.LoginInput{Tenant: "t", Username: "admin", Password: "RecoveredPassword123!"}); e != nil {
		t.Fatal("recovered password cannot log in", e)
	}
}

func TestOfflineAgentEnrollmentRequiresHumanOwner(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.yaml")
	if e := os.WriteFile(overlay, []byte(dbOverlay(dir)), 0600); e != nil {
		t.Fatal(e)
	}
	common := []string{"-base", "../../base.yaml", "-config", overlay, "-tenant", "t", "-operator", "test", "-reason", "test"}
	var out, errs bytes.Buffer
	bootstrap := append(append([]string{}, common...), "-mode", "bootstrap", "-password-stdin", "-subject", "owner")
	if code := run(context.Background(), bootstrap, password(), &out, &errs); code != 0 {
		t.Fatal(code, errs.String())
	}
	enroll := append(append([]string{}, common...), "-mode", "enroll-agent", "-subject", "agent", "-owner", "owner", "-credential-file", filepath.Join(dir, "agent.json"))
	if code := run(context.Background(), enroll, password(), &out, &errs); code != 0 {
		t.Fatal(code, errs.String())
	}
	if st, e := os.Stat(filepath.Join(dir, "agent.json")); e != nil || st.Mode().Perm() != 0600 || strings.Contains(out.String(), "Secret") {
		t.Fatal("agent credential file", e)
	}
	if code := run(context.Background(), enroll, password(), &out, &errs); code == 0 {
		t.Fatal("overwrote an existing credential file")
	}
	args := append(append([]string{}, common...), "-mode", "enroll-agent", "-subject", "bad-agent", "-credential-file", filepath.Join(dir, "bad.json"))
	if code := run(context.Background(), args, password(), &out, &errs); code == 0 {
		t.Fatal("agent without a human owner enrolled")
	}
	if _, e := os.Stat(filepath.Join(dir, "bad.json")); !os.IsNotExist(e) {
		t.Fatal("failed enrollment left credential file")
	}
}

// dbOverlay keeps the real default graph and only points SQL at a private file.
func dbOverlay(dir string) string {
	return "sql:\n  config:\n    dsn: " + filepath.Join(dir, "db") + "\n"
}

func password() io.Reader { return strings.NewReader("AdminPassword123!\n") }

// The browser has no credential login, so the operator sets the password here.
func TestAdminModesRequirePasswordOnStdinOnly(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.yaml")
	if e := os.WriteFile(overlay, []byte(dbOverlay(dir)), 0600); e != nil {
		t.Fatal(e)
	}
	common := []string{"-base", "../../base.yaml", "-config", overlay, "-tenant", "t", "-subject", "admin", "-operator", "test", "-reason", "test"}
	var out, errs bytes.Buffer
	if code := run(context.Background(), append([]string{"-mode", "bootstrap"}, common...), password(), &out, &errs); code != 2 {
		t.Fatal("bootstrap without -password-stdin accepted", code)
	}
	if code := run(context.Background(), append([]string{"-mode", "bootstrap", "-password-stdin"}, common...), strings.NewReader("short\n"), &out, &errs); code != 2 {
		t.Fatal("short password accepted", code)
	}
	if code := run(context.Background(), append([]string{"-mode", "bootstrap", "-password-stdin"}, common...), password(), &out, &errs); code != 0 {
		t.Fatal(code, errs.String())
	}
	sec, done := open(t, overlay)
	defer done()
	if _, e := sec.Login(context.Background(), capsecurity.LoginInput{Tenant: "t", Username: "admin", Password: "AdminPassword123!"}); e != nil {
		t.Fatal("bootstrap password cannot log in", e)
	}
}

// open builds the real graph on the test database and returns its security service.
func open(t *testing.T, overlay string) (*security.Service, func()) {
	t.Helper()
	_, graph, e := config.BuildFiles(context.Background(), "../../base.yaml", overlay)
	if e != nil {
		t.Fatal(e)
	}
	storage, _ := build.GetByID[lifecycle.Resource](graph, "sql")
	if e = storage.Open(context.Background()); e != nil {
		t.Fatal(e)
	}
	sec, _ := build.GetByID[*security.Service](graph, "security")
	return sec, func() { storage.Close(context.Background()) }
}
