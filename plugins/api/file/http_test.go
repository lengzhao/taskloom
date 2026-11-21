package file_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/plugins/security"
	"github.com/lengzhao/taskloom/runtime/config"
)

type fixture struct {
	t                   *testing.T
	srv                 *httptest.Server
	dir                 string
	owner, agent, other string
}

// setup serves the real default graph with a 64-byte upload limit.
func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.yaml")
	raw, _ := json.Marshal(map[string]any{
		"sql":   map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}},
		"blobs": map[string]any{"config": map[string]any{"dir": filepath.Join(dir, "files")}},
		"files": map[string]any{"config": map[string]any{"maxBytes": 64}},
	})
	if e := os.WriteFile(overlay, raw, 0600); e != nil {
		t.Fatal(e)
	}
	_, graph, e := config.BuildFiles(ctx, "../../../base.yaml", overlay)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"sql", "blobs"} {
		r, _ := build.GetByID[lifecycle.Resource](graph, id)
		if e = r.Open(ctx); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { r.Close(ctx) })
	}
	mux, _ := build.GetByID[http.Handler](graph, "http")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sec, _ := build.GetByID[*security.Service](graph, "security")
	if e := sec.Bootstrap(ctx, security.AdminInput{Tenant: "t", Subject: "owner", Operator: "test", Reason: "fixture", Password: "AdminPassword123!"}); e != nil {
		t.Fatal(e)
	}
	issued, e := sec.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: "owner", Password: "AdminPassword123!"})
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{t: t, srv: srv, dir: filepath.Join(dir, "files"), owner: issued.Secret}
	for i, name := range []string{"agent", "other"} {
		if e = sec.Create(ctx, f.owner, "create-"+name, identity.Principal{Tenant: "t", ID: name, Kind: "agent", OwnerID: "owner"}); e != nil {
			t.Fatal(e)
		}
		c, e := sec.Issue(ctx, f.owner, "issue-"+name, capsecurity.IssueInput{Tenant: "t", Subject: name, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			f.agent = c.Secret
		} else {
			f.other = c.Secret
		}
	}
	return f
}

func (f *fixture) do(method, path, token, key, contentType string, body []byte) (int, []byte) {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+"/v1"+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		f.t.Fatal(e)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
func (f *fixture) json(method, path, token, key string, in, out any) int {
	f.t.Helper()
	raw, _ := json.Marshal(in)
	code, b := f.do(method, path, token, key, "application/json", raw)
	if out != nil && code == 200 {
		if e := json.Unmarshal(b, out); e != nil {
			f.t.Fatal(e, string(b))
		}
	}
	return code
}

type file struct {
	ID, Name, MediaType, Digest string
	Size                        int64
}

func (f *fixture) upload(taskID, token, key, name string, body []byte) (int, file) {
	f.t.Helper()
	code, b := f.do("POST", "/tasks/"+taskID+"/files?name="+name, token, key, "text/plain", body)
	var out file
	if code == 200 {
		_ = json.Unmarshal(b, &out)
	}
	return code, out
}

// objects counts stored blobs, excluding the store's own tmp directory.
// The check is relative to the store root: on Linux t.TempDir itself lives
// under /tmp, so matching the absolute path would exclude everything.
func (f *fixture) objects() int {
	n := 0
	_ = filepath.WalkDir(f.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rel, rerr := filepath.Rel(f.dir, path); rerr == nil && !strings.HasPrefix(rel, "tmp"+string(filepath.Separator)) {
			n++
		}
		return nil
	})
	return n
}

func (f *fixture) task(title string) string {
	var created struct{ Task struct{ ID string } }
	if code := f.json("POST", "/tasks", f.owner, "task-"+title, map[string]string{"Title": title, "Goal": "g", "Criteria": "c"}, &created); code != 200 {
		f.t.Fatal("create task", code)
	}
	return created.Task.ID
}

func TestUploadLimitsReplayAndAccess(t *testing.T) {
	f := setup(t)
	id := f.task("a")

	var lim struct{ MaxBytes, MaxFiles, MaxLinks int64 }
	if code := f.json("GET", "/files/limits", f.other, "", nil, &lim); code != 200 || lim.MaxBytes != 64 || lim.MaxFiles != 20 || lim.MaxLinks != 20 {
		t.Fatal("limits", code, lim)
	}
	if code := f.json("GET", "/files/limits", "bad-token", "", nil, nil); code != 401 {
		t.Fatal("limits without login", code)
	}

	code, up := f.upload(id, f.owner, "u1", "notes.txt", []byte("hello"))
	if code != 200 || up.Size != 5 || up.Name != "notes.txt" || len(up.Digest) != 64 {
		t.Fatal("upload", code, up)
	}
	// Same key and bytes replays the original; the duplicate bytes are removed.
	if code, again := f.upload(id, f.owner, "u1", "notes.txt", []byte("hello")); code != 200 || again.ID != up.ID {
		t.Fatal("replay", code, again)
	}
	if code, _ := f.upload(id, f.owner, "u1", "notes.txt", []byte("other")); code != 409 {
		t.Fatal("same key, other bytes", code)
	}
	if code, _ := f.upload(id, f.owner, "big", "big.txt", bytes.Repeat([]byte("x"), 65)); code != 413 {
		t.Fatal("over limit", code)
	}
	if code, _ := f.upload(id, f.owner, "noname", "", []byte("x")); code != 400 {
		t.Fatal("empty name", code)
	}
	if n := f.objects(); n != 1 {
		t.Fatal("stored objects", n)
	}

	// An Agent not in the task can neither upload nor read.
	if code, _ := f.upload(id, f.other, "o1", "x.txt", []byte("x")); code != 403 {
		t.Fatal("outsider upload", code)
	}
	if code, _ := f.do("GET", "/tasks/"+id+"/files/"+up.ID, f.other, "", "", nil); code != 403 {
		t.Fatal("outsider download", code)
	}
	if code, _ := f.do("GET", "/tasks/"+id+"/files", f.other, "", "", nil); code != 403 {
		t.Fatal("outsider list", code)
	}
	// A file belongs to one task: another task cannot read or reference it.
	other := f.task("b")
	if code, _ := f.do("GET", "/tasks/"+other+"/files/"+up.ID, f.owner, "", "", nil); code != 404 {
		t.Fatal("cross-task download", code)
	}
	if code := f.json("POST", "/tasks/"+other+"/commits", f.owner, "c-cross", map[string]any{"Files": []string{up.ID}}, nil); code != 404 {
		t.Fatal("cross-task reference", code)
	}

	// A comment may carry only a file and a link.
	var ref struct{ CommitID string }
	if code := f.json("POST", "/tasks/"+id+"/commits", f.owner, "c1", map[string]any{"Files": []string{up.ID}, "Links": []map[string]string{{"URL": "https://example.com", "Title": "x"}}}, &ref); code != 200 {
		t.Fatal("comment with file", code)
	}
	for _, bad := range []map[string]any{
		{},
		{"Links": []map[string]string{{"URL": "javascript:alert(1)"}}},
		{"Files": []string{up.ID, up.ID}},
	} {
		if code := f.json("POST", "/tasks/"+id+"/commits", f.owner, "bad", bad, nil); code != 400 {
			t.Fatal("invalid comment accepted", bad, code)
		}
	}
	var d struct {
		Commits []struct {
			ID    string
			Files []string
		}
	}
	f.json("GET", "/tasks/"+id, f.owner, "", nil, &d)
	last := d.Commits[len(d.Commits)-1]
	if last.ID != ref.CommitID || len(last.Files) != 1 || last.Files[0] != up.ID {
		t.Fatal("timeline", d.Commits)
	}
	var listed []file
	if f.json("GET", "/tasks/"+id+"/files", f.owner, "", nil, &listed); len(listed) != 1 {
		t.Fatal("list", listed)
	}
}

// A result may be files only; a missing file leaves no result behind.
func TestResultDeliversFiles(t *testing.T) {
	f := setup(t)
	id := f.task("r")
	var w struct {
		Execution    *struct{ ID string }
		Result       *struct{ CommitID string }
		ResultCommit *struct{ Files []string }
	}
	if code := f.json("POST", "/tasks/"+id+"/execution/assign", f.owner, "assign", map[string]string{"Subject": "agent"}, nil); code != 200 {
		t.Fatal("assign", code)
	}
	var task struct{ Task struct{ BasisVersion int64 } }
	f.json("GET", "/tasks/"+id, f.agent, "", nil, &task)
	if code := f.json("POST", "/tasks/"+id+"/execution/open", f.agent, "open", map[string]any{"BasisVersion": task.Task.BasisVersion}, &w); code != 200 || w.Execution == nil {
		t.Fatal("open", code)
	}
	exec := w.Execution.ID
	_, up := f.upload(id, f.agent, "u", "out.csv", []byte("a,b\n1,2\n"))
	// Until a commit references it, an upload is its uploader's alone.
	if code, _ := f.do("GET", "/tasks/"+id+"/files/"+up.ID, f.owner, "", "", nil); code != 404 {
		t.Fatal("unreferenced upload readable by others", code)
	}
	var seen []file
	if f.json("GET", "/tasks/"+id+"/files", f.owner, "", nil, &seen); len(seen) != 0 {
		t.Fatal("unreferenced upload listed to others", seen)
	}
	if f.json("GET", "/tasks/"+id+"/files", f.agent, "", nil, &seen); len(seen) != 1 {
		t.Fatal("uploader's own list", seen)
	}
	// Nor can another participant publish it by referencing its ID.
	if code := f.json("POST", "/tasks/"+id+"/commits", f.owner, "c-steal", map[string]any{"Files": []string{up.ID}}, nil); code != 404 {
		t.Fatal("referenced another's unreferenced upload", code)
	}
	if code := f.json("POST", "/tasks/"+id+"/results", f.agent, "r-missing", map[string]any{"ExecutionID": exec, "Files": []string{"0123456789abcdef0123456789abcdef"}}, nil); code != 404 {
		t.Fatal("missing file", code)
	}
	f.json("GET", "/tasks/"+id+"/workflow", f.agent, "", nil, &w)
	if w.Result != nil {
		t.Fatal("result leaked from failed submit")
	}
	if code := f.json("POST", "/tasks/"+id+"/results", f.agent, "r1", map[string]any{"ExecutionID": exec, "Files": []string{up.ID}}, &w); code != 200 || w.ResultCommit == nil || len(w.ResultCommit.Files) != 1 {
		t.Fatal("submit files", code, w.ResultCommit)
	}
	first := w.Result.CommitID
	// Resubmitting the same files keeps the result; other references replace it.
	f.json("POST", "/tasks/"+id+"/results", f.agent, "r2", map[string]any{"ExecutionID": exec, "Files": []string{up.ID}}, &w)
	if w.Result.CommitID != first {
		t.Fatal("same delivery re-registered")
	}
	f.json("POST", "/tasks/"+id+"/results", f.agent, "r3", map[string]any{"ExecutionID": exec, "Files": []string{up.ID}, "Body": "说明"}, &w)
	if w.Result.CommitID == first {
		t.Fatal("changed delivery not registered")
	}
	// The owner reads the delivered bytes.
	code, body := f.do("GET", "/tasks/"+id+"/files/"+up.ID, f.owner, "", "", nil)
	if code != 200 || string(body) != "a,b\n1,2\n" {
		t.Fatal("download", code, string(body))
	}
}
