package fs_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/plugins/storage/fs"
)

func TestPutGetDelete(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := fs.New(fs.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// A temporary file left by an interrupted Put is removed on Open.
	if err = os.MkdirAll(filepath.Join(dir, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "tmp", "put-stale"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	const key = "0123456789abcdef0123456789abcdef"
	if err = s.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, fault.ErrUnavailable) {
		t.Fatal("put before open", err)
	}
	if err = s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if left, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(left) != 0 {
		t.Fatal("stale temporary files kept", left)
	}
	if err = s.Put(ctx, key, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if err = s.Put(ctx, key, strings.NewReader("other")); !errors.Is(err, fault.ErrConflict) {
		t.Fatal("overwrite", err)
	}
	r, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "hello" {
		t.Fatal("content", string(b))
	}
	for _, bad := range []string{"../../etc/passwd", "ABCDEF0123456789", "short"} {
		if err = s.Put(ctx, bad, strings.NewReader("x")); !errors.Is(err, fault.ErrInput) {
			t.Fatal("bad key accepted", bad, err)
		}
	}
	// A failing reader leaves nothing behind and keeps its category.
	if err = s.Put(ctx, "aa"+key[2:], failing{}); !errors.Is(err, fault.ErrTooLarge) {
		t.Fatal("reader error", err)
	}
	if _, err = s.Get(ctx, "aa"+key[2:]); !errors.Is(err, fault.ErrNotFound) {
		t.Fatal("partial object", err)
	}
	if err = s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, key); err != nil {
		t.Fatal("delete is retryable", err)
	}
	if _, err = s.Get(ctx, key); !errors.Is(err, fault.ErrNotFound) {
		t.Fatal("deleted", err)
	}
}

type failing struct{}

func (failing) Read([]byte) (int, error) { return 0, fault.ErrTooLarge }
