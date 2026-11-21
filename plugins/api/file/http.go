// Package file adapts task file upload, listing and download to HTTP.
package file

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/file"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/runtime/httpx"
)

type Config struct {
	// TransferTimeout replaces the server's read or write timeout for one
	// upload or download, which may be far larger than a JSON request.
	TransferTimeout string `json:"transferTimeout"`
}

func (c *Config) SetDefaults() {
	if c.TransferTimeout == "" {
		c.TransferTimeout = "10m"
	}
}
func (c *Config) Validate() error {
	d, err := time.ParseDuration(c.TransferTimeout)
	if err != nil || d <= 0 {
		return errors.New("transferTimeout must be a positive duration")
	}
	return nil
}

type Files interface {
	api.Commands
	api.Queries
}
type Deps struct {
	Files Files `json:"files"`
}
type Module struct {
	timeout time.Duration
	d       Deps
}

func init() {
	pluginkit.Register("api/file", func(c Config, d Deps) (*Module, error) {
		c.SetDefaults()
		if err := c.Validate(); err != nil {
			return nil, err
		}
		if d.Files == nil {
			return nil, fault.ErrInput
		}
		timeout, _ := time.ParseDuration(c.TransferTimeout)
		return &Module{timeout, d}, nil
	})
}
func (m *Module) Routes() []httpapi.Route {
	return []httpapi.Route{
		{Method: "POST", Path: "/tasks/{id}/files", Operation: "file.upload", Handler: http.HandlerFunc(m.upload)},
		{Method: "GET", Path: "/tasks/{id}/files", Operation: "file.list", Handler: http.HandlerFunc(m.list)},
		{Method: "GET", Path: "/tasks/{id}/files/{fid}", Operation: "file.read", Handler: http.HandlerFunc(m.download)},
		{Method: "DELETE", Path: "/tasks/{id}/files/{fid}", Operation: "file.delete", Handler: http.HandlerFunc(m.remove)},
		{Method: "GET", Path: "/files/limits", Operation: "file.limits", Handler: http.HandlerFunc(m.limits)},
	}
}

// upload takes the raw bytes as the body, the name from ?name= and the media
// type from Content-Type.
func (m *Module) upload(w http.ResponseWriter, r *http.Request) {
	// The server's write deadline starts when headers are read, so a long
	// upload extends both: otherwise its stored file gets no response.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(m.timeout))
	_ = rc.SetWriteDeadline(time.Now().Add(m.timeout))
	in := api.Upload{TaskID: r.PathValue("id"), Name: r.URL.Query().Get("name"), MediaType: r.Header.Get("Content-Type")}
	f, err := m.d.Files.Upload(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"), in, r.Body)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, f)
}

// remove withdraws the caller's own upload that no commit references yet.
func (m *Module) remove(w http.ResponseWriter, r *http.Request) {
	taskID, fileID := r.PathValue("id"), r.PathValue("fid")
	if err := m.d.Files.Delete(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"), taskID, fileID); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, struct{ TaskID, FileID string }{taskID, fileID})
}
func (m *Module) limits(w http.ResponseWriter, r *http.Request) {
	l, err := m.d.Files.Limits(r.Context(), httpx.Credential(r))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, l)
}
func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	fs, err := m.d.Files.List(r.Context(), httpx.Credential(r), r.PathValue("id"))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, fs)
}

// download always serves an attachment in a sandbox, so uploaded HTML or SVG
// never runs as this origin's page.
func (m *Module) download(w http.ResponseWriter, r *http.Request) {
	f, body, err := m.d.Files.Open(r.Context(), httpx.Credential(r), r.PathValue("id"), r.PathValue("fid"))
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	defer body.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(m.timeout))
	h := w.Header()
	h.Set("Content-Type", f.MediaType)
	h.Set("Content-Length", strconv.FormatInt(f.Size, 10))
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": f.Name})
	if disposition == "" {
		disposition = "attachment"
	}
	h.Set("Content-Disposition", disposition)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("ETag", `"`+f.Digest+`"`)
	// Exactly Size bytes were registered. A short or failed copy means the
	// stored object differs; abort the connection so the client sees an error
	// instead of a complete-looking response.
	if n, err := io.CopyN(w, body, f.Size); err != nil || n != f.Size {
		panic(http.ErrAbortHandler)
	}
}
