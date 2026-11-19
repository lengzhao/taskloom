package workspace_test

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
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	"github.com/lengzhao/taskloom/domain/security"
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/runtime/config"
)

func TestHTTPAuthenticatedWorkspaceLoop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.yaml")
	raw, _ := json.Marshal(map[string]any{"sql": map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}}})
	if e := os.WriteFile(overlay, raw, 0600); e != nil {
		t.Fatal(e)
	}
	_, graph, e := config.BuildFiles(ctx, "../../../base.yaml", overlay)
	if e != nil {
		t.Fatal(e)
	}
	storage, ok := build.GetByID[lifecycle.Resource](graph, "sql")
	if !ok {
		t.Fatal("sql missing")
	}
	if e = storage.Open(ctx); e != nil {
		t.Fatal(e)
	}
	defer storage.Close(ctx)
	sec, _ := build.GetByID[*security.Service](graph, "security")
	issued, e := sec.Bootstrap(ctx, security.BootstrapInput{Tenant: "t", Subject: "u", Operator: "test", Reason: "test", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}

	module, _ := build.GetByID[httpapi.Module](graph, "workspace-api")
	mux := http.NewServeMux()
	for _, r := range module.Routes() {
		mux.Handle(r.Method+" /v1"+r.Path, r.Handler)
	}
	var cookie *http.Cookie
	request := func(method, path string, body any, key string, bearer bool) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("X-Taskloom-Request", "1")
		r.Header.Set("Origin", "http://127.0.0.1:8080")
		if bearer {
			r.Header.Set("Authorization", "Bearer "+issued.Secret)
		} else if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/v1/app", nil, "", false); w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("显式授予访问权限")) || bytes.Contains(w.Body.Bytes(), []byte("/grants")) {
		t.Fatal("legacy authorization UI remains")
	}
	if w := request("POST", "/v1/grants", map[string]any{"Action": "task.read"}, "old-api", true); w.Code != 404 {
		t.Fatal("legacy grant route exists", w.Code)
	}
	if e = sec.Create(ctx, issued.Secret, "new-member", identity.Principal{Tenant: "t", ID: "member", Kind: "human"}); e != nil {
		t.Fatal(e)
	}
	if w := request("POST", "/v1/members", map[string]any{"Tenant": "t", "Subject": "member", "Role": "member", "Active": false}, "deactivate", true); w.Code != 200 {
		t.Fatal("member management", w.Code, w.Body.String())
	}
	if w := request("POST", "/v1/members", map[string]any{"Tenant": "t", "Subject": "member", "Role": "member", "Active": true, "Action": "task.edit"}, "arbitrary-permission", true); w.Code != 400 {
		t.Fatal("arbitrary action accepted", w.Code)
	}
	if w := request("GET", "/v1/tasks", nil, "", false); w.Code != 401 {
		t.Fatalf("anonymous %d", w.Code)
	}
	w := request("POST", "/v1/session", nil, "", true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie = w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Value == issued.Secret {
		t.Fatal("session cookie protection")
	}
	w = request("POST", "/v1/tasks", map[string]any{"Title": "可用闭环"}, "create", false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct{ Task struct{ ID string } }
	if e = json.Unmarshal(w.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	path := "/v1/tasks/" + created.Task.ID
	w = request("GET", path+"/candidates", nil, "", false)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"Subject":"u"`)) || bytes.Contains(w.Body.Bytes(), []byte(`"Subject":"member"`)) || bytes.Contains(w.Body.Bytes(), []byte("Digest")) {
		t.Fatal("candidate route", w.Code, w.Body.String())
	}
	w = request("POST", path+"/commits", map[string]string{"Body": "评论"}, "comment", false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", path, nil, "", false)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("评论")) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("POST", path+"/commits", map[string]string{"Body": "伪造", "Kind": "result"}, "invalid", false)
	if w.Code != 400 {
		t.Fatal("arbitrary kind accepted", w.Code)
	}

	if e = sec.Revoke(ctx, issued.Secret, "revoke", security.RevokeInput{Tenant: "t", CredentialID: issued.ID}); e != nil {
		t.Fatal(e)
	}
	w = request("GET", path, nil, "", false)
	if w.Code != 401 {
		t.Fatal("revoked credential", w.Code)
	}

	oldCookie := cookie
	w = request("DELETE", "/v1/session", nil, "", false)
	if w.Code != 200 {
		t.Fatal("logout", w.Code)
	}
	cookie = oldCookie
	w = request("GET", path, nil, "", false)
	if w.Code != 401 {
		t.Fatal("logged out cookie reusable", w.Code)
	}
	r := httptest.NewRequest("POST", "/v1/tasks", io.NopCloser(bytes.NewReader([]byte(`{"Title":"csrf"}`))))
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("csrf allowed")
	}
	r = httptest.NewRequest("GET", "/v1/tasks", nil)
	r.Header.Set("Origin", "https://evil.invalid")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("origin allowed")
	}
}
