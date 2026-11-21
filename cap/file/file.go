// Package file registers immutable file versions on a task. A File row exists
// only after its bytes are stored; a commit refers to files by ID, and a new
// upload is always a new version with a new ID.
package file

import (
	"context"
	"io"
	"time"
)

// File is one stored version. Digest is the SHA-256 of its bytes in hex.
// Uploader and OwnerID are the authenticated principal and its owner.
type File struct {
	Tenant, TaskID, ID string
	Name, MediaType    string
	Size               int64
	Digest             string
	Uploader           string
	OwnerID            string `json:",omitempty"`
	CreatedAt          time.Time
}

// Store registers versions and reads, within one task, those visible to
// viewer: files a commit on the task references, and files viewer uploaded.
// An upload is shared only by referencing it; until then a file outside that
// set is fault.ErrNotFound.
type Store interface {
	RegisterFile(ctx context.Context, value File) error
	GetFile(ctx context.Context, tenant, taskID, id, viewer string) (File, error)
	ListFiles(ctx context.Context, tenant, taskID, viewer string) ([]File, error)
}

// Upload names the bytes a caller sends. MediaType is advisory metadata;
// downloads are always served as attachments.
type Upload struct{ TaskID, Name, MediaType string }

// Commands stores bytes, then registers them under the caller's task access.
// The same idempotency key with the same bytes returns the original File.
type Commands interface {
	Upload(ctx context.Context, token, key string, in Upload, body io.Reader) (File, error)
}

// Limits are the per-commit attachment limits and this deployment's upload
// size cap, so a client can refuse what would be rejected before sending it.
type Limits struct{ MaxBytes, MaxFiles, MaxLinks int64 }

// Queries read files with the caller's current task read access, limited to
// what Store shows the caller. Limits needs only an authenticated caller.
type Queries interface {
	List(ctx context.Context, token, taskID string) ([]File, error)
	Open(ctx context.Context, token, taskID, fileID string) (File, io.ReadCloser, error)
	Limits(ctx context.Context, token string) (Limits, error)
}
