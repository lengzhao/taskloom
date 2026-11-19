package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lengzhao/taskloom/cap/record"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	"github.com/lengzhao/taskloom/cap/unitofwork"
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

	if e = sec.Revoke(ctx, issued.Secret, "revoke", capsecurity.RevokeInput{Tenant: "t", CredentialID: issued.ID}); e != nil {
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

func TestHTTPMemberAdministration(t *testing.T) {
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
	admin, e := sec.Bootstrap(ctx, security.BootstrapInput{Tenant: "t", Subject: "admin", Operator: "test", Reason: "fixture", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	module, _ := build.GetByID[httpapi.Module](graph, "workspace-api")
	mux := http.NewServeMux()
	for _, r := range module.Routes() {
		mux.Handle(r.Method+" /v1"+r.Path, r.Handler)
	}
	request := func(token, method, path, key string, body any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/v1"+path, bytes.NewReader(b))
		r.Header.Set("Origin", "http://127.0.0.1:8080")
		r.Header.Set("X-Taskloom-Request", "1")
		r.Header.Set("Idempotency-Key", key)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	must := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status %d expected %d", w.Code, status)
		}
	}
	must(request("", "GET", "/members", "", nil), 401)
	me := request(admin.Secret, "GET", "/me", "", nil)
	must(me, 200)
	if !bytes.Contains(me.Body.Bytes(), []byte(`"CanAdmin":true`)) {
		t.Fatal("admin entry missing")
	}
	must(request(admin.Secret, "POST", "/identities", "create-user", map[string]any{"Subject": "user", "Kind": "human", "Password": "userPassword123!"}), 200)
	must(request(admin.Secret, "POST", "/identities", "cross-tenant", map[string]any{"Subject": "foreign", "Kind": "human", "Tenant": "other"}), 400)
	must(request(admin.Secret, "POST", "/identities", "privilege", map[string]any{"Subject": "evil", "Kind": "human", "Role": "admin"}), 400)
	must(request(admin.Secret, "POST", "/identities", "missing-owner", map[string]any{"Subject": "agent", "Kind": "agent"}), 400)
	must(request(admin.Secret, "POST", "/identities", "create-agent", map[string]any{"Subject": "agent", "Kind": "agent", "OwnerID": "user"}), 200)
	expires := time.Now().Add(time.Hour)
	input := map[string]any{"Subject": "user", "ExpiresAt": expires}
	must(request(admin.Secret, "POST", "/credentials", "issue-user", input), 400)
	user, e := sec.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: "user", Password: "userPassword123!"})
	if e != nil {
		t.Fatal(e)
	}
	must(request(user.Secret, "POST", "/identities", "user-create", map[string]any{"Subject": "intruder", "Kind": "human"}), 403)
	must(request(user.Secret, "GET", "/members", "", nil), 403)
	must(request(user.Secret, "POST", "/credentials", "user-issue", input), 403)
	userMe := request(user.Secret, "GET", "/me", "", nil)
	must(userMe, 200)
	if !bytes.Contains(userMe.Body.Bytes(), []byte(`"CanAdmin":false`)) {
		t.Fatal("member admin flag")
	}
	must(request(admin.Secret, "POST", "/credentials", "bad-agent-credential", map[string]any{"Subject": "agent", "ExpiresAt": expires}), 400)
	agentInput := map[string]any{"Subject": "agent", "InstanceID": "machine", "Incarnation": "boot", "Purpose": "business", "ExpiresAt": expires}
	agentResp := request(admin.Secret, "POST", "/credentials", "issue-agent", agentInput)
	must(agentResp, 200)
	var agent capsecurity.Issued
	json.Unmarshal(agentResp.Body.Bytes(), &agent)
	replay := request(admin.Secret, "POST", "/credentials", "issue-agent", agentInput)
	must(replay, 200)
	var repeated capsecurity.Issued
	json.Unmarshal(replay.Body.Bytes(), &repeated)
	if repeated.Secret != "" || repeated.ID != agent.ID {
		t.Fatal("replay redisclosed/reissued secret")
	}

	must(request(agent.Secret, "GET", "/members", "", nil), 403)
	must(request(agent.Secret, "POST", "/identities", "agent-create", map[string]any{"Subject": "intruder", "Kind": "human"}), 403)
	list := request(admin.Secret, "GET", "/members", "", nil)
	must(list, 200)
	if !bytes.Contains(list.Body.Bytes(), []byte(`"Subject":"agent"`)) || bytes.Contains(list.Body.Bytes(), []byte(user.Secret)) || bytes.Contains(list.Body.Bytes(), []byte("Secret")) {
		t.Fatal("directory metadata or credential leak")
	}
	must(request(admin.Secret, "POST", "/credentials/revoke", "revoke", map[string]any{"CredentialID": agent.ID}), 200)
	must(request(agent.Secret, "GET", "/me", "", nil), 401)
	must(request(admin.Secret, "POST", "/members", "disable-user", map[string]any{"Tenant": "t", "Subject": "user", "Role": "member", "Active": false}), 200)
	must(request(user.Secret, "GET", "/me", "", nil), 403)
	must(request(admin.Secret, "POST", "/credentials", "inactive-issue", input), 400)
	must(request(admin.Secret, "POST", "/identities", "inactive-owner", map[string]any{"Subject": "other-agent", "Kind": "agent", "OwnerID": "user"}), 403)
	list = request(admin.Secret, "GET", "/members", "", nil)
	must(list, 200)
	if !bytes.Contains(list.Body.Bytes(), []byte(`"Active":false`)) {
		t.Fatal("inactive member missing from management")
	}
	must(request(admin.Secret, "POST", "/members", "self-disable", map[string]any{"Tenant": "t", "Subject": "admin", "Role": "admin", "Active": false}), 403)
	must(request(admin.Secret, "POST", "/members", "reactivate-user", map[string]any{"Tenant": "t", "Subject": "user", "Role": "member", "Active": true}), 200)
	must(request(user.Secret, "GET", "/me", "", nil), 200)
	// Foreign principals are never enumerated in this administrator's tenant.
	principals, _ := build.GetByID[identity.Principals](graph, "sql")
	tx, _ := build.GetByID[unitofwork.Manager](graph, "sql")
	if e = tx.Do(ctx, func(ctx context.Context) error {
		return principals.CreatePrincipal(ctx, identity.Principal{Tenant: "other", ID: "foreign", Kind: "human"})
	}); e != nil {
		t.Fatal(e)
	}
	foreign, e := sec.Recover(ctx, security.BootstrapInput{Tenant: "other", Subject: "foreign", Operator: "test", Reason: "isolation", ExpiresAt: expires})
	if e != nil {
		t.Fatal(e)
	}
	foreignList := request(foreign.Secret, "GET", "/members", "", nil)
	must(foreignList, 200)
	if bytes.Contains(foreignList.Body.Bytes(), []byte(`"Subject":"user"`)) {
		t.Fatal("cross tenant directory leak")
	}
}

func TestPasswordLoginCreationResetAndLegacyAdminTransition(t *testing.T) {
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
	admin, e := sec.Bootstrap(ctx, security.BootstrapInput{Tenant: "t", Subject: "admin", Operator: "test", Reason: "fixture", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	module, _ := build.GetByID[httpapi.Module](graph, "workspace-api")
	mux := http.NewServeMux()
	for _, r := range module.Routes() {
		mux.Handle(r.Method+" /v1"+r.Path, r.Handler)
	}
	request := func(method, path, key string, body any, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/v1"+path, bytes.NewReader(b))
		r.Header.Set("Origin", "http://127.0.0.1:8080")
		r.Header.Set("X-Taskloom-Request", "1")
		r.Header.Set("Idempotency-Key", key)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	must := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("status %d, expected %d", w.Code, code)
		}
	}
	legacy := request("POST", "/session", "", nil, admin.Secret, nil)
	must(legacy, 200)
	oldAdminCookie := legacy.Result().Cookies()[0]
	must(request("POST", "/account/password", "initial", map[string]string{"Password": "AdminPassword123!"}, "", oldAdminCookie), 200)
	must(request("GET", "/me", "", nil, "", oldAdminCookie), 401)
	must(request("POST", "/session", "", nil, admin.Secret, nil), 401)
	login := func(username, password string) *httptest.ResponseRecorder {
		return request("POST", "/session", "", map[string]string{"Username": username, "Password": password}, "", nil)
	}
	must(login("admin", "wrong-password"), 401)
	logged := login("admin", "AdminPassword123!")
	must(logged, 200)
	if bytes.Contains(logged.Body.Bytes(), []byte("Secret")) {
		t.Fatal("password login returned a bearer secret")
	}
	adminCookie := logged.Result().Cookies()[0]
	if !adminCookie.HttpOnly || adminCookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	userInput := map[string]string{"Subject": "alice", "Kind": "human", "Password": "AlicePassword123!"}
	must(request("POST", "/identities", "alice", userInput, "", adminCookie), 200)
	must(request("POST", "/identities", "alice", userInput, "", adminCookie), 200)
	userInput["Password"] = "differentPassword123!"
	must(request("POST", "/identities", "alice", userInput, "", adminCookie), 409)
	userLogin := login("alice", "AlicePassword123!")
	must(userLogin, 200)
	userCookie := userLogin.Result().Cookies()[0]
	must(request("GET", "/members", "", nil, "", userCookie), 403)
	must(request("POST", "/users/admin/password", "escalate", map[string]string{"Password": "OtherPassword123!"}, "", userCookie), 403)
	must(request("POST", "/account/password", "wrong-current", map[string]string{"CurrentPassword": "wrong", "Password": "NewPassword123!"}, "", userCookie), 403)
	must(request("POST", "/account/password", "change", map[string]string{"CurrentPassword": "AlicePassword123!", "Password": "NewPassword123!"}, "", userCookie), 200)
	must(request("GET", "/me", "", nil, "", userCookie), 401)
	must(login("alice", "AlicePassword123!"), 401)
	changed := login("alice", "NewPassword123!")
	must(changed, 200)
	newCookie := changed.Result().Cookies()[0]
	must(request("POST", "/users/alice/password", "reset", map[string]string{"Password": "ResetPassword123!"}, "", adminCookie), 200)
	must(request("GET", "/me", "", nil, "", newCookie), 401)
	must(login("alice", "NewPassword123!"), 401)
	reset := login("alice", "ResetPassword123!")
	must(reset, 200)
	resetCookie := reset.Result().Cookies()[0]
	must(request("DELETE", "/session", "", nil, "", resetCookie), 200)
	must(request("GET", "/me", "", nil, "", resetCookie), 401)
	must(request("POST", "/members", "disable", map[string]any{"Tenant": "t", "Subject": "alice", "Role": "member", "Active": false}, "", adminCookie), 200)
	must(login("alice", "ResetPassword123!"), 401)
	passwords, _ := build.GetByID[identity.Passwords](graph, "sql")
	hash, e := passwords.PasswordHash(ctx, "t", "alice")
	if e != nil || !strings.HasPrefix(hash, "pbkdf2-sha256$600000$") || strings.Contains(hash, "ResetPassword123!") {
		t.Fatal("password storage")
	}
	receipts, _ := build.GetByID[record.Receipts](graph, "sql")
	receipt, e := receipts.FindReceipt(ctx, record.RequestKey{Tenant: "t", Subject: "admin", Operation: "identity.create", Key: "alice"})
	if e != nil || bytes.Contains(receipt.Result, []byte("AlicePassword")) {
		t.Fatal("password receipt")
	}
	// IP-bounded login attempts, including malformed/incorrect passwords.
	limited := false
	for i := 0; i < 21; i++ {
		w := login("admin", "bad")
		if w.Code == 429 {
			limited = w.Header().Get("Retry-After") != ""
			break
		}
		must(w, 401)
	}
	if !limited {
		t.Fatal("login rate limit missing")
	}
}
