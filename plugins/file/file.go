// Package file uploads and reads task file versions. Bytes go to the blob
// store first and are registered in SQL afterwards; the two share no
// transaction, so a failed or replayed registration deletes the new bytes.
package file

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"mime"
	"time"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/blob"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/file"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/domain/attachment"
	"github.com/lengzhao/taskloom/runtime/command"
)

type Config struct {
	// MaxBytes caps one uploaded file.
	MaxBytes int64 `json:"maxBytes"`
}

const DefaultMaxBytes = 100 << 20

func (c *Config) SetDefaults() {
	if c.MaxBytes == 0 {
		c.MaxBytes = DefaultMaxBytes
	}
}
func (c *Config) Validate() error {
	if c.MaxBytes < 1 {
		return errors.New("maxBytes must be positive")
	}
	return nil
}

// Deps has six fields, reviewed: uploads run the command lifecycle (commands)
// and register the version (files) after storing bytes (blobs); reads and the
// pre-upload access check authenticate (identity) and check task access
// (authorization) in a read transaction (transactions). None of them is a
// service locator, and splitting reads out would duplicate the same check.
type Deps struct {
	Commands      capcommand.Handler     `json:"commands"`
	Transactions  unitofwork.Manager     `json:"transactions"`
	Identity      identity.Authenticator `json:"identity"`
	Authorization az.Authorizer          `json:"authorization"`
	Files         api.Store              `json:"files"`
	Blobs         blob.Store             `json:"blobs"`
}
type Module struct {
	c Config
	d Deps
}

func init() { pluginkit.Register("commands/file", New) }
func New(c Config, d Deps) (*Module, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if d.Commands == nil || d.Transactions == nil || d.Identity == nil || d.Authorization == nil || d.Files == nil || d.Blobs == nil {
		return nil, errors.New("file capabilities required")
	}
	return &Module{c, d}, nil
}

// pinned is what an upload's idempotency key fixes: the request and the bytes.
type pinned struct {
	api.Upload
	Size   int64
	Digest string
}
type ref struct{ TaskID, FileID string }

func (m *Module) Upload(ctx context.Context, token, key string, in api.Upload, body io.Reader) (api.File, error) {
	name, ok := attachment.FileName(in.Name)
	if !ok {
		return api.File{}, fault.Input("invalid_name", "name")
	}
	if key == "" || len(key) > 128 {
		return api.File{}, fault.Input(fault.CodeIdempotencyKeyRequired, "Idempotency-Key")
	}
	if in.TaskID == "" {
		return api.File{}, fault.Input(fault.CodeRequired, "TaskID")
	}
	mediaType, err := cleanMediaType(in.MediaType)
	if err != nil {
		return api.File{}, err
	}
	in.Name, in.MediaType = name, mediaType
	// Check access before accepting bytes; the command checks it again.
	var caller identity.Principal
	err = m.d.Transactions.Do(ctx, func(tx context.Context) error {
		var e error
		caller, e = m.principal(tx, token, "file.upload", in.TaskID)
		return e
	})
	if err != nil {
		return api.File{}, err
	}
	id := newID()
	counted := &counter{r: body, max: m.c.MaxBytes, h: sha256.New()}
	if err = m.d.Blobs.Put(ctx, id, counted); err != nil {
		return api.File{}, err
	}
	in2 := pinned{Upload: in, Size: counted.n, Digest: hex.EncodeToString(counted.h.Sum(nil))}
	applied := false
	b, err := command.Run(ctx, m.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "file.upload", Resource: in.TaskID}, in2, func(tx context.Context, p identity.Principal) ([]byte, error) {
		f := api.File{Tenant: p.Tenant, TaskID: in.TaskID, ID: id, Name: name, MediaType: mediaType, Size: in2.Size, Digest: in2.Digest, Uploader: p.ID, OwnerID: p.OwnerID, CreatedAt: time.Now().UTC()}
		if e := m.d.Files.RegisterFile(tx, f); e != nil {
			return nil, e
		}
		applied = true
		return json.Marshal(ref{TaskID: in.TaskID, FileID: id})
	})
	if err != nil || !applied {
		// Compensate: these bytes are unregistered (failure) or a duplicate of
		// the original upload (replay). A failed delete leaves unreachable bytes.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = m.d.Blobs.Delete(cleanup, id)
		cancel()
	}
	if err != nil {
		return api.File{}, err
	}
	var r ref
	if err = json.Unmarshal(b, &r); err != nil {
		return api.File{}, err
	}
	// The command authenticated the same credential: same tenant and uploader,
	// and a replay's receipt belongs to that same subject.
	return m.d.Files.GetFile(ctx, caller.Tenant, r.TaskID, r.FileID, caller.ID)
}

func (m *Module) List(ctx context.Context, token, taskID string) ([]api.File, error) {
	var out []api.File
	err := m.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := m.principal(tx, token, "task.read", taskID)
		if e != nil {
			return e
		}
		out, e = m.d.Files.ListFiles(tx, p.Tenant, taskID, p.ID)
		return e
	})
	return out, err
}

func (m *Module) Limits(ctx context.Context, token string) (api.Limits, error) {
	err := m.d.Transactions.Do(ctx, func(tx context.Context) error {
		_, e := m.d.Identity.Authenticate(tx, token)
		return e
	})
	if err != nil {
		return api.Limits{}, err
	}
	return api.Limits{MaxBytes: m.c.MaxBytes, MaxFiles: attachment.MaxFiles, MaxLinks: attachment.MaxLinks}, nil
}

// Open checks read access, then opens the bytes outside the SQL transaction.
func (m *Module) Open(ctx context.Context, token, taskID, fileID string) (api.File, io.ReadCloser, error) {
	var f api.File
	err := m.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := m.principal(tx, token, "task.read", taskID)
		if e != nil {
			return e
		}
		f, e = m.d.Files.GetFile(tx, p.Tenant, taskID, fileID, p.ID)
		return e
	})
	if err != nil {
		return api.File{}, nil, err
	}
	r, err := m.d.Blobs.Get(ctx, f.ID)
	if errors.Is(err, fault.ErrNotFound) {
		// The version is registered, so missing bytes are a storage failure.
		err = fault.ErrUnavailable
	}
	if err != nil {
		return api.File{}, nil, err
	}
	return f, r, nil
}

func (m *Module) principal(ctx context.Context, token, op, taskID string) (identity.Principal, error) {
	p, e := m.d.Identity.Authenticate(ctx, token)
	if e != nil {
		return p, e
	}
	return p, m.d.Authorization.Check(ctx, p, op, taskID)
}

func cleanMediaType(v string) (string, error) {
	if v == "" {
		return "application/octet-stream", nil
	}
	mt, params, err := mime.ParseMediaType(v)
	if err != nil || len(v) > 255 {
		return "", fault.Input(fault.CodeInvalid, "Content-Type")
	}
	return mime.FormatMediaType(mt, params), nil
}

func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return hex.EncodeToString(b[:])
}

// counter hashes and counts what it reads, failing once max is exceeded.
type counter struct {
	r   io.Reader
	max int64
	n   int64
	h   hash.Hash
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.max {
		return 0, fault.ErrTooLarge
	}
	c.h.Write(p[:n])
	return n, err
}
