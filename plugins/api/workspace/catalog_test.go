package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/lengzhao/pluginkit/build"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	cs "github.com/lengzhao/taskloom/cap/security"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	api "github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/domain/security"
	"github.com/lengzhao/taskloom/runtime/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWorkspaceProjectsLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.json")
	raw, _ := json.Marshal(map[string]any{"sql": map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}}})
	if e := os.WriteFile(overlay, raw, 0600); e != nil {
		t.Fatal(e)
	}
	_, graph, e := config.BuildFiles(ctx, "../../../base.yaml", overlay)
	if e != nil {
		t.Fatal(e)
	}
	storage, _ := build.GetByID[lifecycle.Resource](graph, "sql")
	if e = storage.Open(ctx); e != nil {
		t.Fatal(e)
	}
	defer storage.Close(ctx)
	sec, _ := build.GetByID[*security.Service](graph, "security")
	admin, e := sec.Bootstrap(ctx, security.BootstrapInput{Tenant: "t", Subject: "admin", Operator: "test", Reason: "test", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	if e = sec.CreateUser(ctx, admin.Secret, "user", cs.UserInput{Tenant: "t", Username: "alice", Password: "test-password-123"}); e != nil {
		t.Fatal(e)
	}
	alice, e := sec.Login(ctx, cs.LoginInput{Tenant: "t", Username: "alice", Password: "test-password-123"})
	if e != nil {
		t.Fatal(e)
	}
	mod, _ := build.GetByID[httpapi.Module](graph, "workspace-api")
	mux := http.NewServeMux()
	for _, r := range mod.Routes() {
		mux.Handle(r.Method+" /v1"+r.Path, r.Handler)
	}
	call := func(token, method, path, key string, body any, code int) []byte {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/v1"+path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Origin", "http://127.0.0.1:8080")
		r.Header.Set("X-Taskloom-Request", "1")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != code {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	call("", "GET", "/space", "", nil, 401)
	var space catalog.Space
	json.Unmarshal(call(admin.Secret, "GET", "/space", "", nil, 200), &space)
	if space.Version != 0 || space.Name != "t" {
		t.Fatal(space)
	}
	edit := catalog.SpaceEdit{Name: "Engineering", ExpectedVersion: 0}
	call(alice.Secret, "PATCH", "/space", "s1", edit, 403)
	first := call(admin.Secret, "PATCH", "/space", "s1", edit, 200)
	if !bytes.Equal(first, call(admin.Secret, "PATCH", "/space", "s1", edit, 200)) {
		t.Fatal("space replay changed")
	}
	call(admin.Secret, "PATCH", "/space", "s2", edit, 409)
	var project catalog.Project
	in := catalog.ProjectEdit{Name: "Launch", Owner: "alice"}
	json.Unmarshal(call(alice.Secret, "POST", "/projects", "p1", in, 200), &project)
	var replay catalog.Project
	json.Unmarshal(call(alice.Secret, "POST", "/projects", "p1", in, 200), &replay)
	if project.ID == "" || replay.ID != project.ID {
		t.Fatal("project replay")
	}
	var other catalog.Project
	json.Unmarshal(call(admin.Secret, "POST", "/projects", "p2", catalog.ProjectEdit{Name: "Admin project"}, 200), &other)
	call(alice.Secret, "PATCH", "/projects/"+other.ID, "denied", catalog.ProjectEdit{Name: "Steal", Owner: "alice", ExpectedVersion: 1}, 403)
	var task api.Detail
	json.Unmarshal(call(alice.Secret, "POST", "/tasks", "t1", api.Create{Title: "Visible", ProjectID: project.ID}, 200), &task)
	call(admin.Secret, "POST", "/tasks", "hidden", api.Create{Title: "Admin private", ProjectID: project.ID}, 200)
	var visible []api.Detail
	json.Unmarshal(call(alice.Secret, "GET", "/tasks", "", nil, 200), &visible)
	if len(visible) != 1 {
		t.Fatalf("project leaked tasks: %d", len(visible))
	}
	call(alice.Secret, "POST", "/tasks/"+task.Task.ID+"/project", "move", api.ProjectMove{ProjectID: other.ID, ExpectedVersion: task.Task.Version}, 200)
	var moved api.Detail
	json.Unmarshal(call(alice.Secret, "GET", "/tasks/"+task.Task.ID, "", nil, 200), &moved)
	if moved.Task.BasisVersion != task.Task.BasisVersion || !reflect.DeepEqual(moved.Position, task.Position) || moved.Task.ProjectID != other.ID {
		t.Fatal("move changed workflow/basis")
	}
	call(alice.Secret, "POST", "/tasks/"+task.Task.ID+"/project", "stale", api.ProjectMove{ExpectedVersion: task.Task.Version}, 409)
	archive := catalog.ProjectEdit{Name: other.Name, Owner: other.Owner, Archived: true, ExpectedVersion: 1}
	call(admin.Secret, "PATCH", "/projects/"+other.ID, "archive", archive, 200)
	call(alice.Secret, "POST", "/tasks", "archived", api.Create{Title: "No", ProjectID: other.ID}, 409)
	call(alice.Secret, "POST", "/tasks/"+task.Task.ID+"/project", "out", api.ProjectMove{ExpectedVersion: moved.Task.Version}, 200)
	call(admin.Secret, "PATCH", "/projects/"+other.ID, "stale-project", archive, 409)
	archive.Archived = false
	archive.ExpectedVersion = 2
	call(admin.Secret, "PATCH", "/projects/"+other.ID, "restore", archive, 200)
	// Concurrent edits of one project cannot silently overwrite each other.
	svc, _ := build.GetByID[catalog.Service](graph, "catalog")
	concurrent, e := svc.SaveProject(ctx, admin.Secret, "race-create", catalog.ProjectEdit{Name: "Race"})
	if e != nil {
		t.Fatal(e)
	}
	errs := make(chan error, 2)
	for _, key := range []string{"race-a", "race-b"} {
		go func(key string) {
			_, err := svc.SaveProject(ctx, admin.Secret, key, catalog.ProjectEdit{ID: concurrent.ID, Name: key, Owner: "admin", ExpectedVersion: 1})
			errs <- err
		}(key)
	}
	wins, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-errs
		if err == nil {
			wins++
		} else if errors.Is(err, fault.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("concurrent edits: wins=%d conflicts=%d", wins, conflicts)
	}
	// A project belonging to another tenant is neither listed nor usable as a task reference.
	store, _ := build.GetByID[catalog.Store](graph, "sql")
	tx, _ := build.GetByID[unitofwork.Manager](graph, "sql")
	if e = tx.Do(ctx, func(ctx context.Context) error {
		return store.SaveProject(ctx, catalog.Project{Tenant: "other", ID: "foreign", Name: "Hidden", Owner: "admin", Version: 1}, 0)
	}); e != nil {
		t.Fatal(e)
	}
	call(alice.Secret, "POST", "/tasks", "foreign", api.Create{Title: "No", ProjectID: "foreign"}, 404)
	if bytes.Contains(call(alice.Secret, "GET", "/projects", "", nil, 200), []byte("foreign")) {
		t.Fatal("foreign project leaked")
	}
	// Revocation still applies before an idempotent replay.
	if e = sec.SetMember(ctx, admin.Secret, "disable", az.Member{Tenant: "t", Subject: "alice", Role: "member", Active: false}); e != nil {
		t.Fatal(e)
	}
	call(alice.Secret, "POST", "/projects", "p1", in, 403)
	call(alice.Secret, "GET", "/projects", "", nil, 403)
	// Runtime-only Agent credentials cannot read catalog metadata or mutate projects.
	if e = sec.Create(ctx, admin.Secret, "agent", identity.Principal{Tenant: "t", ID: "agent", Kind: "agent", OwnerID: "admin"}); e != nil {
		t.Fatal(e)
	}
	runtime, e := sec.Issue(ctx, admin.Secret, "runtime", cs.IssueInput{Tenant: "t", Subject: "agent", InstanceID: "test", Incarnation: "1", Purpose: "runtime", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	call(runtime.Secret, "GET", "/projects", "", nil, 403)
	call(runtime.Secret, "POST", "/projects", "agent-create", in, 403)
}
