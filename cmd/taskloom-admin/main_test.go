package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapAndRecoveryUseExclusivePrivateFile(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	credential := filepath.Join(dir, "credential.json")
	yaml := "server:\n  use: server\n  deps:\n    resources: [sql]\n    components: []\nsql:\n  use: storage/sql\n  config:\n    driver: sqlite\n    dsn: " + filepath.Join(dir, "db") + "\n"
	if err := os.WriteFile(base, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-base", base, "-config", "", "-mode", "bootstrap", "-tenant", "t", "-subject", "admin", "-operator", "operator", "-reason", "initial", "-credential-file", credential}
	var out, errs bytes.Buffer
	if code := run(context.Background(), args, &out, &errs); code != 0 {
		t.Fatalf("code=%d %s", code, &errs)
	}
	secret, err := os.ReadFile(credential)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(credential)
	if st.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", st.Mode())
	}
	if strings.Contains(out.String(), "Secret") || len(secret) == 0 {
		t.Fatal("unexpected credential output")
	}
	if code := run(context.Background(), args, &out, &errs); code == 0 {
		t.Fatal("overwrote credential or repeated bootstrap")
	}
	after, _ := os.ReadFile(credential)
	if !bytes.Equal(secret, after) {
		t.Fatal("credential overwritten")
	}
	args[5] = "recover"
	args[len(args)-1] = filepath.Join(dir, "recovered.json")
	if code := run(context.Background(), args, &out, &errs); code != 0 {
		t.Fatalf("recovery: %d %s", code, &errs)
	}
}
