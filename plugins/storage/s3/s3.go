// Package s3 stores file bytes in an S3-compatible bucket (AWS S3, MinIO). It
// implements the same cap/blob contract as the local directory store and
// replaces that node in configuration.
//
// Credentials never appear in configuration: they come from the environment
// (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY, optionally AWS_SESSION_TOKEN) or
// from the instance's IAM role.
package s3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Config struct {
	// Endpoint is host[:port] without scheme, such as s3.amazonaws.com.
	Endpoint string `json:"endpoint"`
	// Insecure uses plain HTTP; allowed only for a loopback endpoint.
	Insecure bool   `json:"insecure"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	// Prefix is prepended to every object name, so one bucket can be shared.
	Prefix string `json:"prefix"`
}

func (c *Config) Validate() error {
	if c.Endpoint == "" || strings.Contains(c.Endpoint, "/") {
		return errors.New("s3 endpoint must be host[:port] without scheme")
	}
	if c.Bucket == "" {
		return errors.New("s3 bucket required")
	}
	if c.Insecure && !loopback(c.Endpoint) {
		return errors.New("s3 insecure allowed only for a loopback endpoint")
	}
	for _, r := range c.Prefix {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./", r)) {
			return errors.New("s3 prefix may contain only letters, digits and -_./")
		}
	}
	return nil
}

func loopback(endpoint string) bool {
	host := endpoint
	if i := strings.LastIndex(endpoint, ":"); i >= 0 && !strings.HasSuffix(endpoint, "]") {
		host = endpoint[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

type Store struct {
	c      Config
	client *minio.Client
	mu     sync.RWMutex
	open   bool
}

func init() { pluginkit.Register("storage/s3", New) }

// New only prepares the client; it opens no connection.
func New(c Config) (*Store, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	creds := credentials.NewChainCredentials([]credentials.Provider{
		&credentials.EnvAWS{},
		&credentials.IAM{Client: &http.Client{Transport: http.DefaultTransport}},
	})
	client, err := minio.New(c.Endpoint, &minio.Options{Creds: creds, Secure: !c.Insecure, Region: c.Region})
	if err != nil {
		return nil, errors.New("s3 client configuration invalid")
	}
	return &Store{c: c, client: client}, nil
}

// Open checks that the bucket is reachable with the configured credentials.
// Uploads that failed midway are aborted by the client; parts left by a
// cancelled request (whose abort runs on the cancelled context) or a crashed
// process are removed by the bucket's lifecycle rule for incomplete multipart
// uploads, which the deployment must configure.
func (s *Store) Open(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open {
		return errors.New("blob store already open")
	}
	ok, err := s.client.BucketExists(ctx, s.c.Bucket)
	if err != nil || !ok {
		return fault.ErrUnavailable
	}
	s.open = true
	return nil
}
func (s *Store) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open = false
	return nil
}

func (s *Store) ready() error {
	if !s.open {
		return fault.ErrUnavailable
	}
	return nil
}

// object maps a key to <prefix><first two chars>/<key>. Keys are generated
// lower-hex IDs; anything else is rejected like the directory store does.
func (s *Store) object(key string) (string, error) {
	if len(key) < 8 || len(key) > 128 {
		return "", fault.ErrInput
	}
	for _, r := range key {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "", fault.ErrInput
		}
	}
	return s.c.Prefix + key[:2] + "/" + key, nil
}

// Put streams the body with the client's multipart upload, which buffers one
// part at a time, becomes visible only when it completes and is aborted on any
// failure. Keys are fresh random 128-bit IDs, so no existing object is replaced.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ready(); err != nil {
		return err
	}
	name, err := s.object(key)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, s.c.Bucket, name, r, -1, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A reader-side rejection (such as a size limit) keeps its category.
	for _, known := range []error{fault.ErrInput, fault.ErrTooLarge} {
		if errors.Is(err, known) {
			return known
		}
	}
	return fault.ErrUnavailable
}

// Get opens the object; a missing object is reported before returning, so a
// caller never starts a response for bytes that are not there.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ready(); err != nil {
		return nil, err
	}
	name, err := s.object(key)
	if err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.c.Bucket, name, minio.GetObjectOptions{})
	if err == nil {
		_, err = obj.Stat()
	}
	if err != nil {
		if obj != nil {
			_ = obj.Close()
		}
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, fault.ErrNotFound
		}
		return nil, fault.ErrUnavailable
	}
	return obj, nil
}

// Delete removes the object; S3 reports success for a missing key too.
func (s *Store) Delete(ctx context.Context, key string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ready(); err != nil {
		return err
	}
	name, err := s.object(key)
	if err != nil {
		return err
	}
	if err = s.client.RemoveObject(ctx, s.c.Bucket, name, minio.RemoveObjectOptions{}); err != nil {
		return fault.ErrUnavailable
	}
	return nil
}
