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
//
// Bytes that no File row owns are tracked as pending blobs, so a failed or
// interrupted upload and a deleted file always leave a retryable record:
// ReserveBlob records a key before its bytes are written; RegisterFile removes
// that reservation in the same transaction, failing with fault.ErrConflict when
// it is gone or claimed for cleanup; DeleteFile removes an uploader's own
// unreferenced file and records its key as pending. Referenced files are kept
// with the commits that reference them: deleting one is fault.ErrConflict.
type Store interface {
	ReserveBlob(ctx context.Context, key string) error
	RegisterFile(ctx context.Context, value File) error
	DeleteFile(ctx context.Context, tenant, taskID, id, uploader string) error
	GetFile(ctx context.Context, tenant, taskID, id, viewer string) (File, error)
	ListFiles(ctx context.Context, tenant, taskID, viewer string) ([]File, error)
	PendingBlobs
}

// PendingBlobs hands out keys whose bytes no File row owns. ClaimBlobs marks
// keys recorded before the given time as being cleaned up and returns them; a
// claimed reservation can no longer be registered, so its bytes may be deleted
// even while a slow upload is still writing them. A claimed key is returned
// again by a later claim until ForgetBlob removes it after its bytes are gone.
type PendingBlobs interface {
	ClaimBlobs(ctx context.Context, before time.Time, limit int) ([]string, error)
	ForgetBlob(ctx context.Context, key string) error
}

// Upload names the bytes a caller sends. MediaType is advisory metadata;
// downloads are always served as attachments.
type Upload struct{ TaskID, Name, MediaType string }

// Commands stores bytes, then registers them under the caller's task access.
// The same idempotency key with the same bytes returns the original File.
// Delete withdraws the caller's own upload that no commit references yet;
// a referenced file is fault.ErrConflict, anything else fault.ErrNotFound.
type Commands interface {
	Upload(ctx context.Context, token, key string, in Upload, body io.Reader) (File, error)
	Delete(ctx context.Context, token, key, taskID, fileID string) error
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
