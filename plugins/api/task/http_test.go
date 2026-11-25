package task_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/plugins/security"
	"github.com/lengzhao/taskloom/runtime/config"
)

type problem struct{ Code, Message, Field string }

// serve runs the real default graph and returns a caller bound to the owner.
func serve(t *testing.T) func(method, path, key string, body any) (int, []byte) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.yaml")
	raw, _ := json.Marshal(map[string]any{
		"sql":   map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}},
		"blobs": map[string]any{"config": map[string]any{"dir": filepath.Join(dir, "files")}},
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
	return func(method, path, key string, body any) (int, []byte) {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+"/v1"+path, r)
		req.Header.Set("Authorization", "Bearer "+issued.Secret)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
}

// Errors name what to fix: a stable code, a message and the field at fault.
func TestErrorsExplainTheFix(t *testing.T) {
	call := serve(t)
	for _, c := range []struct {
		name, method, path, key string
		body                    any
		status                  int
		want                    problem
	}{
		{"missing key", "POST", "/tasks", "", map[string]any{"Title": "x"}, 400, problem{Code: "idempotency_key_required", Field: "Idempotency-Key"}},
		{"unknown field", "POST", "/tasks", "k1", map[string]any{"Title": "x", "Foo": 1}, 400, problem{Code: "unknown_field", Field: "Foo"}},
		{"wrong type", "POST", "/tasks", "k2", map[string]any{"Title": 1}, 400, problem{Code: "field_invalid", Field: "Title"}},
		{"missing title", "POST", "/tasks", "k3", map[string]any{"Goal": "g"}, 400, problem{Code: "field_required", Field: "Title"}},
	} {
		status, raw := call(c.method, c.path, c.key, c.body)
		var got problem
		if json.Unmarshal(raw, &got) != nil || status != c.status || got.Code != c.want.Code || got.Field != c.want.Field || got.Message == "" {
			t.Fatalf("%s: %d %s", c.name, status, raw)
		}
	}

	// A task with a goal and no criteria is ready to work on; the write answers with the view.
	status, raw := call("POST", "/tasks", "create", map[string]any{"Title": "写周报", "Goal": "汇总本周进展"})
	var v struct {
		Task struct {
			ID           string
			BasisVersion int64
			Tenant       *string
		}
		Position  struct{ State, Waiting string }
		NextActor string
		Viewer    string
		Commits   []struct{ Kind string }
		CanEdit   bool
	}
	if status != 200 || json.Unmarshal(raw, &v) != nil || v.Task.ID == "" || v.Task.Tenant != nil || v.Position.State != "executing" || v.Position.Waiting != "等待负责人分配处理人" || v.NextActor != "owner" || v.Viewer != "owner" || len(v.Commits) != 1 || !v.CanEdit {
		t.Fatalf("create view %d %s", status, raw)
	}
	id := v.Task.ID

	// Submitting without the execution names the missing field.
	status, raw = call("POST", "/tasks/"+id+"/results", "r0", map[string]any{"Body": "done"})
	var p problem
	if json.Unmarshal(raw, &p); status != 400 || p.Code != "field_required" || p.Field != "ExecutionID" {
		t.Fatalf("submit without execution %d %s", status, raw)
	}
	// An edit on a stale basis says the requirements changed.
	if status, raw = call("PATCH", "/tasks/"+id, "e0", map[string]any{"Title": "x", "BasisVersion": 9}); status != 409 || json.Unmarshal(raw, &p) != nil || p.Code != "basis_changed" {
		t.Fatalf("stale edit %d %s", status, raw)
	}
	// Reusing a key for a different request is not a state conflict to refresh away.
	if status, raw = call("POST", "/tasks", "create", map[string]any{"Title": "另一个"}); status != 409 || json.Unmarshal(raw, &p) != nil || p.Code != "idempotency_key_reused" {
		t.Fatalf("reused key %d %s", status, raw)
	}

	// The list carries everything a row shows; mine keeps the caller's work.
	var rows []struct {
		Task      struct{ ID string }
		NextActor string
		ForMe     bool
		UpdatedAt string
	}
	if status, raw = call("GET", "/tasks?mine=true", "", nil); status != 200 || json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || !rows[0].ForMe || rows[0].NextActor != "owner" || rows[0].UpdatedAt == "" {
		t.Fatalf("mine %d %s", status, raw)
	}
	// Assigning to self starts work; the response is the same view.
	if status, raw = call("POST", "/tasks/"+id+"/assign", "a1", map[string]any{"Subject": "owner"}); status != 200 || json.Unmarshal(raw, &v) != nil || v.NextActor != "assignee" || v.Task.ID != id {
		t.Fatalf("assign %d %s", status, raw)
	}
	if status, raw = call("GET", "/tasks?mine=maybe", "", nil); status != 400 || json.Unmarshal(raw, &p) != nil || p.Field != "mine" {
		t.Fatalf("bad mine %d %s", status, raw)
	}
	// A blocked action carries its explanation, the same text an error would.
	var blocked struct {
		Blocked map[string]struct{ Code, Message string }
	}
	if status, raw = call("GET", "/tasks/"+id, "", nil); status != 200 || json.Unmarshal(raw, &blocked) != nil {
		t.Fatalf("read %d %s", status, raw)
	}
	for _, r := range blocked.Blocked {
		if r.Code == "" || r.Message == "" {
			t.Fatalf("unexplained block %s", raw)
		}
	}
	// Project errors name their field too.
	if status, raw = call("POST", "/projects", "p0", map[string]any{"Name": " "}); status != 400 || json.Unmarshal(raw, &p) != nil || p.Field != "Name" {
		t.Fatalf("project name %d %s", status, raw)
	}
}

// Tags travel through the vocabulary routes, task creation and the task's
// own tags route; every task write answers with the task view.
func TestTagRoutes(t *testing.T) {
	call := serve(t)
	status, body := call("POST", "/tags", "tag-a", map[string]any{"Name": "安全"})
	var tag struct{ ID, Name string }
	if status != 200 || json.Unmarshal(body, &tag) != nil || tag.Name != "安全" {
		t.Fatal("create tag", status, string(body))
	}
	if status, body = call("POST", "/tags", "tag-dup", map[string]any{"Name": "安全"}); status != 409 {
		t.Fatal("duplicate tag", status, string(body))
	}
	status, body = call("GET", "/tags", "", nil)
	var tags []struct{ ID string }
	if status != 200 || json.Unmarshal(body, &tags) != nil || len(tags) != 1 {
		t.Fatal("list tags", status, string(body))
	}
	status, body = call("POST", "/tasks", "create", map[string]any{"Title": "t", "Tags": []string{tag.ID}})
	var view struct {
		Task struct {
			ID   string
			Tags []string
		}
	}
	if status != 200 || json.Unmarshal(body, &view) != nil || len(view.Task.Tags) != 1 {
		t.Fatal("create tagged task", status, string(body))
	}
	var p problem
	status, body = call("POST", "/tasks", "bad", map[string]any{"Title": "t", "Tags": []string{"nope"}})
	if status != 400 || json.Unmarshal(body, &p) != nil || p.Code != "tag_unknown" || p.Field != "Tags" {
		t.Fatal("unknown tag", status, string(body))
	}
	status, body = call("POST", "/tasks/"+view.Task.ID+"/tags", "untag", map[string]any{"Remove": []string{tag.ID}})
	view.Task.Tags = nil
	if status != 200 || json.Unmarshal(body, &view) != nil || len(view.Task.Tags) != 0 {
		t.Fatal("remove tag", status, string(body))
	}
}
