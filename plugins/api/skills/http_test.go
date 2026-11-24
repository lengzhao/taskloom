package skills_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/plugins/security"
	"github.com/lengzhao/taskloom/runtime/config"
	"gopkg.in/yaml.v3"
)

// topics are the fake embedding space: one axis per word, plus a bias so
// unrelated texts are not orthogonal-zero vectors.
var topics = []string{"pdf", "slides", "database"}

type fixture struct {
	t                   *testing.T
	srv                 *httptest.Server
	dir                 string
	owner, agent, other string
	embedDown           *atomic.Bool
}

// setup wires the example overlay plus a fake embedder and semantic search.
func setup(t *testing.T) *fixture { return setupWith(t, true) }

func setupWith(t *testing.T, semantic bool) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	down := &atomic.Bool{}
	embedder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var in struct{ Input []string }
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []item `json:"data"`
		}{}
		for i, text := range in.Input {
			v := []float32{0.05, 0, 0, 0}
			for j, word := range topics {
				if strings.Contains(strings.ToLower(text), word) {
					v[j+1] = 1
				}
			}
			out.Data = append(out.Data, item{i, v})
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(embedder.Close)

	raw, err := os.ReadFile("../../../examples/skills/skills.config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var overlay map[string]any
	if err = yaml.Unmarshal(raw, &overlay); err != nil {
		t.Fatal(err)
	}
	overlay["sql"] = map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}}
	overlay["blobs"] = map[string]any{"config": map[string]any{"dir": filepath.Join(dir, "files")}}
	if semantic {
		overlay["embedder"] = map[string]any{"use": "embedding/openai", "config": map[string]any{"url": embedder.URL + "/v1/embeddings", "model": "fake", "dimensions": 4}}
		for _, id := range []string{"skills", "skill-query"} {
			node := overlay[id].(map[string]any)
			node["deps"].(map[string]any)["embedder"] = "embedder"
			if id == "skill-query" {
				node["config"] = map[string]any{} // default modes: all four
			}
		}
	}
	b, _ := json.Marshal(overlay)
	path := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	_, graph, err := config.BuildFiles(ctx, "../../../base.yaml", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sql", "blobs"} {
		r, _ := build.GetByID[lifecycle.Resource](graph, id)
		if err = r.Open(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close(ctx) })
	}
	mux, _ := build.GetByID[http.Handler](graph, "http")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sec, _ := build.GetByID[*security.Service](graph, "security")
	if err = sec.Bootstrap(ctx, security.AdminInput{Tenant: "t", Subject: "owner", Operator: "test", Reason: "fixture", Password: "AdminPassword123!"}); err != nil {
		t.Fatal(err)
	}
	issued, err := sec.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: "owner", Password: "AdminPassword123!"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, srv: srv, dir: filepath.Join(dir, "files"), owner: issued.Secret, embedDown: down}
	for i, name := range []string{"agent", "other"} {
		if err = sec.Create(ctx, f.owner, "create-"+name, identity.Principal{Tenant: "t", ID: name, Kind: "agent", OwnerID: "owner"}); err != nil {
			t.Fatal(err)
		}
		c, err := sec.Issue(ctx, f.owner, "issue-"+name, capsecurity.IssueInput{Tenant: "t", Subject: name, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			f.agent = c.Secret
		} else {
			f.other = c.Secret
		}
	}
	return f
}

func (f *fixture) do(method, path, token, key, contentType string, body []byte) (int, []byte, http.Header) {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+"/v1/skills"+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func (f *fixture) json(method, path, token, key string, in, out any) int {
	f.t.Helper()
	var raw []byte
	if in != nil {
		raw, _ = json.Marshal(in)
	}
	code, b, _ := f.do(method, path, token, key, "application/json", raw)
	if out != nil && (code == 200 || (code == 400 && bytes.HasPrefix(b, []byte("{")))) {
		if err := json.Unmarshal(b, out); err != nil {
			f.t.Fatal(err, string(b))
		}
	}
	return code
}

type release struct {
	Ref, CapabilityID, Name, Version, Digest, Publisher, Trust string
	Size                                                       int64
}

func (f *fixture) publish(token, key string, zipped []byte) (int, release, []byte) {
	f.t.Helper()
	code, b, _ := f.do("POST", "/capabilities", token, key, "application/zip", zipped)
	var r release
	if code == 200 {
		if err := json.Unmarshal(b, &r); err != nil {
			f.t.Fatal(err)
		}
	}
	return code, r, b
}

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

func skill(t *testing.T, name, version, description string, extra ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	md, _ := w.Create(name + "/SKILL.md")
	io.WriteString(md, "---\nname: "+name+"\ndescription: "+description+"\nmetadata:\n  skilla:\n    version: "+version+"\n---\n# "+name+"\n")
	for _, e := range extra {
		x, _ := w.Create(name + "/" + e)
		io.WriteString(x, e)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type page struct {
	Items []struct {
		ID, Name string
		Release  struct{ Ref, Version string }
		Score    float64
	}
	Total int
}

func (f *fixture) search(token, query string) (int, page) {
	f.t.Helper()
	var p page
	code := f.json("GET", "/capabilities?"+query, token, "", nil, &p)
	return code, p
}

var workspaceHost = map[string]any{"Contract": "agent-skill-host@1.0.0", "Affordances": []string{"agent-skills", "filesystem", "outbound-network"}}

func TestPublishDiscoverReadAndDecide(t *testing.T) {
	f := setup(t)
	pdf := skill(t, "pdf-reader", "1.0.0", "Reads PDF documents and extracts their text.")
	code, rel, body := f.publish(f.agent, "p1", pdf)
	if code != 200 {
		t.Fatal("publish", code, string(body))
	}
	sum := sha256.Sum256(pdf)
	if rel.Digest != hex.EncodeToString(sum[:]) || rel.Size != int64(len(pdf)) || rel.Publisher != "agent" || len(rel.Ref) != 32 {
		t.Fatalf("release %+v", rel)
	}
	// Private by default: another member sees nothing, not even existence.
	if code, p := f.search(f.other, ""); code != 200 || p.Total != 0 {
		t.Fatal("private listed", code, p)
	}
	for _, path := range []string{"/capabilities/resolve?name=pdf-reader", "/releases/" + rel.Ref, "/releases/" + rel.Ref + "/artifact"} {
		if code, _, _ := f.do("GET", path, f.other, "", "", nil); code != 404 {
			t.Fatal("private readable", path, code)
		}
	}
	if code := f.json("PATCH", "/capabilities/"+rel.CapabilityID, f.agent, "v1", map[string]string{"Visibility": "tenant"}, nil); code != 200 {
		t.Fatal("visibility", code)
	}
	if code, r, _ := f.publish(f.other, "s1", skill(t, "slides-maker", "0.1.0", "Builds slides for a presentation.")); code != 200 {
		t.Fatal("second publish", code)
	} else if code := f.json("PATCH", "/capabilities/"+r.CapabilityID, f.other, "v2", map[string]string{"Visibility": "tenant"}, nil); code != 200 {
		t.Fatal("visibility 2", code)
	}

	if _, p := f.search(f.other, ""); p.Total != 2 || p.Items[0].Name != "pdf-reader" || p.Items[0].Release.Version != "1.0.0" || p.Items[0].Release.Ref != rel.Ref {
		t.Fatal("browse", p)
	}
	if _, p := f.search(f.other, "match=name&q=pdf-raeder"); p.Total != 1 || p.Items[0].Name != "pdf-reader" {
		t.Fatal("fuzzy name", p)
	}
	if _, p := f.search(f.other, "match=lexical&q=presentation"); p.Total != 1 || p.Items[0].Name != "slides-maker" {
		t.Fatal("lexical", p)
	}
	if _, p := f.search(f.other, "match=semantic&q=make+some+slides"); p.Total != 1 || p.Items[0].Name != "slides-maker" {
		t.Fatal("semantic", p)
	}
	if code, _ := f.search(f.other, "match=semantic&q=x"); code != 400 {
		t.Fatal("one-character semantic query", code)
	}

	var info release
	if code := f.json("GET", "/capabilities/resolve?name=pdf-reader", f.other, "", nil, &info); code != 200 || info.Ref != rel.Ref || info.Trust != "unreviewed" {
		t.Fatal("resolve", code, info)
	}
	code, bytes_, h := f.do("GET", "/releases/"+rel.Ref+"/artifact", f.other, "", "", nil)
	if code != 200 || !bytes.Equal(bytes_, pdf) || h.Get("X-Content-SHA256") != rel.Digest {
		t.Fatal("artifact", code, h)
	}

	var d struct {
		State, Reason, Trust string
		Missing              []string
		Action               *struct{ Kind, Digest string }
	}
	if code := f.json("POST", "/capability-decisions", f.other, "", map[string]any{"ReleaseRef": rel.Ref, "Host": workspaceHost}, &d); code != 200 || d.State != "ready" || d.Action.Kind != "materialize-exact-artifact" || d.Action.Digest != rel.Digest {
		t.Fatal("ready decision", code, d)
	}
	f.json("POST", "/capability-decisions", f.other, "", map[string]any{"ReleaseRef": rel.Ref, "Host": map[string]any{"Contract": "agent-skill-host@1.0.0", "Affordances": []string{"agent-skills"}}}, &d)
	if d.State != "unavailable" || d.Reason != "host-affordance-missing" || len(d.Missing) != 1 || d.Missing[0] != "filesystem" {
		t.Fatal("affordance decision", d)
	}

	// Review: an Agent cannot review; the administrator can, and a rejection blocks delivery.
	if code := f.json("POST", "/admin/releases/"+rel.Ref+"/reviews", f.other, "r0", map[string]string{"Verdict": "approved"}, nil); code != 403 {
		t.Fatal("agent review", code)
	}
	if code := f.json("POST", "/admin/releases/"+rel.Ref+"/reviews", f.owner, "r1", map[string]string{"Verdict": "rejected", "Reason": "unsafe"}, nil); code != 200 {
		t.Fatal("review", code)
	}
	d.Action = nil
	f.json("POST", "/capability-decisions", f.other, "", map[string]any{"ReleaseRef": rel.Ref, "Host": workspaceHost}, &d)
	if d.State != "blocked" || d.Trust != "rejected" || d.Action != nil {
		t.Fatal("rejected decision", d)
	}
	// A replay returns the review its key wrote, not a later one.
	f.json("POST", "/admin/releases/"+rel.Ref+"/reviews", f.owner, "r2", map[string]string{"Verdict": "approved"}, nil)
	var replay struct{ Verdict, Reason string }
	if code := f.json("POST", "/admin/releases/"+rel.Ref+"/reviews", f.owner, "r1", map[string]string{"Verdict": "rejected", "Reason": "unsafe"}, &replay); code != 200 || replay.Verdict != "rejected" || replay.Reason != "unsafe" {
		t.Fatal("review replay", code, replay)
	}
}

func TestVersionsIdempotencyAndCompensation(t *testing.T) {
	f := setup(t)
	v1 := skill(t, "notes", "1.0.0", "Takes notes.")
	_, first, _ := f.publish(f.agent, "k1", v1)
	if _, replay, _ := f.publish(f.agent, "k1", v1); replay.Ref != first.Ref {
		t.Fatal("replay", replay, first)
	}
	if _, retry, _ := f.publish(f.agent, "k2", v1); retry.Ref != first.Ref {
		t.Fatal("identical retry", retry, first)
	}
	var rejected struct{ Diagnostics []struct{ Code string } }
	code, _, b := f.publish(f.agent, "k3", skill(t, "notes", "1.0.0", "Takes notes.", "extra.txt"))
	json.Unmarshal(b, &rejected)
	if code != 400 || len(rejected.Diagnostics) != 1 || rejected.Diagnostics[0].Code != "version-not-increasing" {
		t.Fatal("same version, other bytes", code, string(b))
	}
	if code, _, _ := f.publish(f.agent, "k4", skill(t, "notes", "0.9.0", "Takes notes.")); code != 400 {
		t.Fatal("older version", code)
	}
	if code, _, _ := f.publish(f.agent, "k1", skill(t, "notes", "1.1.0", "Takes notes.")); code != 409 {
		t.Fatal("key reused for other bytes", code)
	}
	_, second, _ := f.publish(f.agent, "k5", skill(t, "notes", "1.1.0", "Takes better notes."))
	var info release
	if f.json("GET", "/capabilities/resolve?name=notes", f.agent, "", nil, &info); info.Ref != second.Ref || info.Version != "1.1.0" {
		t.Fatal("current", info)
	}
	if code := f.json("GET", "/releases/"+first.Ref, f.agent, "", nil, &info); code != 200 || info.Version != "1.0.0" {
		t.Fatal("old release", code, info)
	}
	// Only the two registered releases keep bytes: replays, retries and rejections left none.
	if n := f.objects(); n != 2 {
		t.Fatal("objects", n)
	}

	code, _, b = f.publish(f.agent, "bad", skill(t, "Bad_Name", "1.0.0", "x"))
	if code != 400 || !strings.Contains(string(b), "invalid-skill-contract") {
		t.Fatal("invalid archive", code, string(b))
	}
	var rep struct {
		Publishable bool
		Diagnostics []struct {
			Code     string
			Blocking bool
		}
	}
	if code, b, _ := f.do("POST", "/capabilities/publication-preflight", f.agent, "", "application/zip", skill(t, "notes", "1.2.0", "Takes notes.")); code != 200 {
		t.Fatal("preflight", code)
	} else if json.Unmarshal(b, &rep); !rep.Publishable || len(rep.Diagnostics) == 0 {
		t.Fatal("preflight report", rep)
	}
	_, b, _ = f.do("POST", "/capabilities/publication-preflight", f.agent, "", "application/zip", v1)
	if json.Unmarshal(b, &rep); rep.Publishable {
		t.Fatal("preflight accepted a stale version", string(b))
	}
}

func TestCollaboratorsAndArchive(t *testing.T) {
	f := setup(t)
	_, rel, _ := f.publish(f.agent, "a1", skill(t, "shared", "1.0.0", "Shared skill."))
	if code, _, _ := f.publish(f.other, "o1", skill(t, "shared", "1.1.0", "Shared skill.")); code != 403 {
		t.Fatal("stranger release", code)
	}
	if code := f.json("PUT", "/capabilities/"+rel.CapabilityID+"/collaborators", f.agent, "c1", map[string]any{"Subjects": []string{"other"}}, nil); code != 200 {
		t.Fatal("collaborators", code)
	}
	if code, r, _ := f.publish(f.other, "o2", skill(t, "shared", "1.1.0", "Shared skill.")); code != 200 || r.Publisher != "other" {
		t.Fatal("collaborator release", code, r)
	}
	if code := f.json("PATCH", "/capabilities/"+rel.CapabilityID, f.other, "o3", map[string]string{"Visibility": "tenant"}, nil); code != 403 {
		t.Fatal("collaborator manages", code)
	}
	if code := f.json("POST", "/capabilities/"+rel.CapabilityID+"/archive", f.agent, "a2", nil, nil); code != 200 {
		t.Fatal("archive", code)
	}
	if _, p := f.search(f.agent, ""); p.Total != 0 {
		t.Fatal("archived listed", p)
	}
	if code, _, _ := f.do("GET", "/releases/"+rel.Ref, f.agent, "", "", nil); code != 404 {
		t.Fatal("archived release readable", code)
	}
	if code, _, _ := f.publish(f.agent, "a3", skill(t, "shared", "2.0.0", "Shared skill.")); code != 409 {
		t.Fatal("publish to archived", code)
	}
	// Administrators manage every capability of their tenant.
	if code := f.json("POST", "/capabilities/"+rel.CapabilityID+"/restore", f.owner, "r1", nil, nil); code != 200 {
		t.Fatal("admin restore", code)
	}
	if _, p := f.search(f.other, ""); p.Total != 1 {
		t.Fatal("restored", p)
	}
}

func TestReviewIndependence(t *testing.T) {
	f := setup(t)
	_, rel, _ := f.publish(f.owner, "p", skill(t, "own", "1.0.0", "Own skill."))
	if code := f.json("POST", "/admin/releases/"+rel.Ref+"/reviews", f.owner, "r", map[string]string{"Verdict": "approved"}, nil); code != 403 {
		t.Fatal("self review", code)
	}
}

func TestEmbeddingFailureAndReindex(t *testing.T) {
	f := setup(t)
	f.embedDown.Store(true)
	code, rel, _ := f.publish(f.agent, "p", skill(t, "pdf-tools", "1.0.0", "Splits PDF files."))
	if code != 200 {
		t.Fatal("publication must not depend on embedding", code)
	}
	if code, _ := f.search(f.agent, "match=semantic&q=pdf+pages"); code != 500 {
		t.Fatal("query embedding failure must not fall back", code)
	}
	if _, p := f.search(f.agent, "match=lexical&q=pdf"); p.Total != 1 {
		t.Fatal("lexical without vectors", p)
	}
	f.embedDown.Store(false)
	if _, p := f.search(f.agent, "match=semantic&q=pdf+pages"); p.Total != 0 {
		t.Fatal("unindexed release matched", p)
	}
	if code := f.json("POST", "/admin/reindex", f.agent, "x", nil, nil); code != 403 {
		t.Fatal("agent reindex", code)
	}
	var out struct{ Indexed int }
	if code := f.json("POST", "/admin/reindex", f.owner, "x", nil, &out); code != 200 || out.Indexed != 1 {
		t.Fatal("reindex", code, out)
	}
	if _, p := f.search(f.agent, "match=semantic&q=pdf+pages"); p.Total != 1 || p.Items[0].ID != rel.CapabilityID {
		t.Fatal("reindexed", p)
	}
}

func TestWithoutEmbedder(t *testing.T) {
	f := setupWith(t, false)
	code, rel, _ := f.publish(f.agent, "p", skill(t, "pdf-tools", "1.0.0", "Splits PDF files into pages."))
	if code != 200 {
		t.Fatal("publish without embedder", code)
	}
	f.publish(f.agent, "q", skill(t, "sql-helper", "1.0.0", "Writes database queries."))
	if _, p := f.search(f.agent, "match=bm25&q=pdf+pages"); p.Total != 1 || p.Items[0].ID != rel.CapabilityID || p.Items[0].Score != 1 {
		t.Fatal("bm25", p)
	}
	if _, p := f.search(f.agent, "match=name&q=pdf-tool"); p.Total != 1 {
		t.Fatal("name", p)
	}
	if code, _ := f.search(f.agent, "match=semantic&q=pdf+pages"); code != 400 {
		t.Fatal("semantic is not enabled", code)
	}
	if code := f.json("POST", "/admin/reindex", f.owner, "x", nil, nil); code == 200 {
		t.Fatal("reindex without embedder", code)
	}
}

func TestDeactivatedMemberLosesAccess(t *testing.T) {
	f := setup(t)
	f.publish(f.agent, "p", skill(t, "gone", "1.0.0", "Gone."))
	raw, _ := json.Marshal(map[string]any{"Tenant": "t", "Subject": "agent", "Role": "member"})
	req, _ := http.NewRequest("DELETE", f.srv.URL+"/v1/members", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+f.owner)
	req.Header.Set("Idempotency-Key", "m")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("deactivate", resp.StatusCode)
	}
	if code, _ := f.search(f.agent, ""); code != 403 && code != 401 {
		t.Fatal("deactivated read", code)
	}
}
