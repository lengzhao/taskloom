package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/minio/minio-go/v7"
)

func TestConfigValidation(t *testing.T) {
	ok := Config{Endpoint: "s3.amazonaws.com", Bucket: "b", Prefix: "taskloom/files/"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Config{
		"scheme":        {Endpoint: "https://s3.amazonaws.com", Bucket: "b"},
		"no bucket":     {Endpoint: "s3.amazonaws.com"},
		"insecure":      {Endpoint: "s3.example.com", Bucket: "b", Insecure: true},
		"prefix escape": {Endpoint: "s3.amazonaws.com", Bucket: "b", Prefix: "a b"},
	} {
		if c.Validate() == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	for _, e := range []string{"127.0.0.1:9000", "localhost:9000", "[::1]:9000"} {
		c := Config{Endpoint: e, Bucket: "b", Insecure: true}
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", e, err)
		}
	}
}

func TestKeysAndClosedStore(t *testing.T) {
	s, err := New(Config{Endpoint: "127.0.0.1:1", Insecure: true, Bucket: "b", Prefix: "p/"})
	if err != nil {
		t.Fatal(err)
	}
	if name, err := s.object("abcdef0123"); err != nil || name != "p/ab/abcdef0123" {
		t.Fatalf("%q %v", name, err)
	}
	for _, bad := range []string{"../../etc", "ABCDEF0123", "abc"} {
		if _, err := s.object(bad); !errors.Is(err, fault.ErrInput) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	ctx := context.Background()
	if err := s.Put(ctx, "abcdef0123", bytes.NewReader(nil)); !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("put before open: %v", err)
	}
	// Nothing listens there: Open reports the store unavailable.
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.Open(short); !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("open: %v", err)
	}
}

// TestService runs against a real S3-compatible service when
// TASKLOOM_S3_TEST_ENDPOINT and TASKLOOM_S3_TEST_BUCKET are set, with
// credentials in AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY. The bucket is
// created when missing; objects are written under a random prefix and removed.
func TestService(t *testing.T) {
	endpoint, bucket := os.Getenv("TASKLOOM_S3_TEST_ENDPOINT"), os.Getenv("TASKLOOM_S3_TEST_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Skip("TASKLOOM_S3_TEST_ENDPOINT/TASKLOOM_S3_TEST_BUCKET not set")
	}
	ctx := context.Background()
	s, err := New(Config{Endpoint: endpoint, Insecure: loopback(endpoint), Bucket: bucket, Prefix: "test-" + randomKey()[:8] + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.client.BucketExists(ctx, bucket); !ok {
		if err = s.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)

	roundTrip := func(size int) {
		t.Helper()
		data := make([]byte, size)
		_, _ = rand.Read(data)
		k := randomKey()
		if err := s.Put(ctx, k, bytes.NewReader(data)); err != nil {
			t.Fatalf("put %d: %v", size, err)
		}
		r, err := s.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("get %d: %d bytes, %v", size, len(got), err)
		}
		if err = s.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Get(ctx, k); !errors.Is(err, fault.ErrNotFound) {
			t.Fatalf("get deleted: %v", err)
		}
	}
	roundTrip(0)
	roundTrip(1234)
	roundTrip(40<<20 + 7) // several parts of the client's 16MiB default

	// Deleting a missing key succeeds, so compensation can be retried.
	if err = s.Delete(ctx, randomKey()); err != nil {
		t.Fatal(err)
	}

	// A reader rejection (the upload size limit) keeps its category and
	// leaves no object, also after a part was already uploaded.
	for _, size := range []int{100, 20 << 20} {
		k := randomKey()
		err = s.Put(ctx, k, io.MultiReader(bytes.NewReader(make([]byte, size)), failing{fault.ErrTooLarge}))
		if !errors.Is(err, fault.ErrTooLarge) {
			t.Fatalf("too large after %d: %v", size, err)
		}
		if _, err = s.Get(ctx, k); !errors.Is(err, fault.ErrNotFound) {
			t.Fatalf("partial object after %d: %v", size, err)
		}
	}

	// Uploads rejected by the reader were aborted: no parts are left behind.
	// The probe upload shows the listing does report incomplete uploads.
	core := minio.Core{Client: s.client}
	probe, err := core.NewMultipartUpload(ctx, bucket, s.c.Prefix+"probe", minio.PutObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := core.ListMultipartUploads(ctx, bucket, s.c.Prefix, "", "", "", 100)
	_ = core.AbortMultipartUpload(ctx, bucket, s.c.Prefix+"probe", probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads.Uploads) != 1 || uploads.Uploads[0].UploadID != probe {
		t.Fatalf("incomplete uploads besides the probe: %+v", uploads.Uploads)
	}

	// An interrupted upload (cancelled request) leaves no object. Its abort
	// runs on the cancelled context and may fail, so parts can remain until
	// the bucket's lifecycle rule removes them, like after a crash.
	k := randomKey()
	cctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(make([]byte, 20<<20))
		cancel()
		_ = pw.CloseWithError(context.Canceled)
	}()
	if err = s.Put(cctx, k, pr); err == nil {
		t.Fatal("interrupted put succeeded")
	}
	if _, err = s.Get(ctx, k); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("interrupted object: %v", err)
	}
}

type failing struct{ err error }

func (f failing) Read([]byte) (int, error) { return 0, f.err }

func randomKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
