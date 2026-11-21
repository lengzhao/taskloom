package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lengzhao/agentkit"
	"github.com/lengzhao/agentkit/runtime/rctx"
)

// fakeTaskloom serves the read endpoints the adapter uses from mutable state.
type fakeTaskloom struct {
	mu       sync.Mutex
	down     bool
	events   []change
	inbox    []pending
	commits  map[string][]commitView
	status   map[string]string
	assignee map[string]string
	gone     map[string]bool
	auth     string
}

func newFake() *fakeTaskloom {
	return &fakeTaskloom{commits: map[string][]commitView{}, status: map[string]string{}, assignee: map[string]string{}, gone: map[string]bool{}, auth: "Bearer s"}
}

func (f *fakeTaskloom) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	if r.Header.Get("Authorization") != f.auth {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case path == "/me":
		write(viewer{Tenant: "t", Subject: "bot", Kind: "agent"})
	case path == "/changes":
		from := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			n, err := strconv.Atoi(c)
			if err != nil {
				http.Error(w, "bad cursor", http.StatusBadRequest)
				return
			}
			from = n
		} else {
			from = len(f.events)
		}
		write(changePage{Changes: f.events[from:], Cursor: strconv.Itoa(len(f.events))})
	case path == "/inbox":
		write(f.inbox)
	case strings.HasSuffix(path, "/workflow"):
		id := strings.Split(path, "/")[2]
		if f.gone[id] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s := snapshot{Assignee: f.assignee[id]}
		s.Position.Status = f.status[id]
		write(s)
	case strings.HasPrefix(path, "/tasks/"):
		id := strings.Split(path, "/")[2]
		if f.gone[id] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		d := taskDetail{}
		d.Task.ID, d.Task.Title = id, "title "+id
		for _, c := range f.commits[id] {
			if c.Sequence > after && len(d.Commits) < 100 {
				d.Commits = append(d.Commits, c)
			}
		}
		write(d)
	default:
		http.NotFound(w, r)
	}
}

var seq int64

func (f *fakeTaskloom) commit(task, author, kind, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seq++
	f.commits[task] = append(f.commits[task], commitView{ID: "c" + strconv.FormatInt(seq, 10), Author: author, Kind: kind, Body: body, Sequence: seq})
	f.events = append(f.events, change{TaskID: task, Kind: "task.changed"})
}

func (f *fakeTaskloom) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func newTestPlatform(t *testing.T, f *fakeTaskloom) *Platform {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := Config{BaseURL: srv.URL + "/v1"}
	cfg.SetDefaults()
	p := newPlatform(cfg, newClient(cfg.BaseURL, "Bearer s", time.Second))
	p.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return p
}

// next returns the next event, or "" when the platform would keep polling.
func next(t *testing.T, p *Platform, polls int) (agentkit.MessageEvent, string) {
	t.Helper()
	for i := 0; i < polls; i++ {
		if len(p.queue) > 0 {
			break
		}
		if err := p.step(context.Background()); err != nil {
			p.fail(err)
		}
	}
	if len(p.queue) == 0 {
		return agentkit.MessageEvent{}, ""
	}
	ev, err := p.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ev, ev.Message.Content[0].Text
}

func turn(p *Platform, ev agentkit.MessageEvent, typ agentkit.EventType) {
	env := rctx.ResolveEnvelope(ev, rctx.RoutePolicy{})
	_ = p.Send(context.Background(), rctx.OutboundFromEnvelope(env, typ, nil))
}

func TestWakesOncePerTaskAndIgnoresOwnCommits(t *testing.T) {
	f := newFake()
	f.commit("A", "alice", "comment", "please do A")
	f.set(func() {
		f.inbox = []pending{{TaskID: "A", Title: "A", NextActor: "assignee"}, {TaskID: "B", WaitingOnSubtasks: true}}
	})
	p := newTestPlatform(t, f)

	_, text := next(t, p, 1)
	if !strings.Contains(text, "任务 A") || strings.Contains(text, "please do A") {
		t.Fatalf("first wake should point at the task without replaying history: %q", text)
	}
	if _, text := next(t, p, 1); text != "" {
		t.Fatalf("blocked task must not wake: %q", text)
	}

	f.commit("A", "bot", "comment", "working on it")
	if _, text := next(t, p, 3); text != "" {
		t.Fatalf("own commit woke the agent: %q", text)
	}

	f.commit("A", "alice", "comment", "also cover B")
	_, text = next(t, p, 3)
	if !strings.Contains(text, "alice") || !strings.Contains(text, "also cover B") || strings.Contains(text, "working on it") {
		t.Fatalf("wake should carry only others' new commits: %q", text)
	}
}

func TestCommitsWhileNotWaitingAreReportedWhenItReturns(t *testing.T) {
	f := newFake()
	f.set(func() { f.inbox = []pending{{TaskID: "A", NextActor: "assignee"}} })
	p := newTestPlatform(t, f)
	next(t, p, 1)

	// The agent asks the owner: the task leaves its inbox until the answer.
	f.set(func() { f.inbox = nil })
	f.commit("A", "bot", "question", "which format?")
	f.commit("A", "alice", "comment", "thinking")
	if _, text := next(t, p, 2); text != "" {
		t.Fatalf("woke while waiting on the owner: %q", text)
	}
	f.set(func() { f.inbox = []pending{{TaskID: "A", NextActor: "assignee"}} })
	f.commit("A", "alice", "answer", "markdown")
	_, text := next(t, p, 2)
	if !strings.Contains(text, "thinking") || !strings.Contains(text, "markdown") || strings.Contains(text, "which format") {
		t.Fatalf("answer wake: %q", text)
	}
}

func TestEachTaskHasItsOwnSessionAndRunningTurnsAreTracked(t *testing.T) {
	f := newFake()
	f.set(func() {
		f.inbox = []pending{{TaskID: "A", NextActor: "assignee"}, {TaskID: "B", NextActor: "addressee"}}
	})
	p := newTestPlatform(t, f)
	a, ta := next(t, p, 1)
	b, _ := next(t, p, 1)
	if !strings.Contains(ta, "任务 A") { // map order decides which wakes first
		a, b = b, a
	}
	ca := rctx.ResolveEnvelope(a, rctx.RoutePolicy{}).Conversation
	cb := rctx.ResolveEnvelope(b, rctx.RoutePolicy{}).Conversation
	if ca == "" || ca == cb {
		t.Fatalf("tasks share a session: %q %q", ca, cb)
	}
	turn(p, a, agentkit.EventTurnStart)
	p.mu.Lock()
	busyA, busyB := p.tasks["A"].busy, p.tasks["B"].busy
	p.mu.Unlock()
	if !busyA || busyB {
		t.Fatalf("busy A=%v B=%v", busyA, busyB)
	}
	turn(p, a, agentkit.EventTurnEnd)
	if p.tasks["A"].busy {
		t.Fatal("turn end not tracked")
	}
}

func TestStopsRunningTurnOnCancelReplaceOrLostAccess(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(f *fakeTaskloom)
	}{
		{"cancel", func(f *fakeTaskloom) { f.status["A"] = "cancelled" }},
		{"replace", func(f *fakeTaskloom) { f.assignee["A"] = "other" }},
		{"access", func(f *fakeTaskloom) { f.gone["A"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.set(func() {
				f.inbox = []pending{{TaskID: "A", NextActor: "assignee"}}
				f.status["A"], f.assignee["A"] = "active", "bot"
			})
			p := newTestPlatform(t, f)
			a, _ := next(t, p, 1)
			turn(p, a, agentkit.EventTurnStart)

			// Waiting on someone else while running is not a reason to stop.
			f.set(func() { f.inbox = nil })
			f.commit("A", "bot", "result", "done")
			if _, text := next(t, p, 2); text != "" {
				t.Fatalf("stopped a finishing turn: %q", text)
			}

			f.set(func() { tc.apply(f) })
			f.set(func() { f.events = append(f.events, change{TaskID: "A", Kind: "task.changed"}) })
			ev, text := next(t, p, 2)
			if text != "/stop" {
				t.Fatalf("want /stop, got %q", text)
			}
			if rctx.ResolveEnvelope(ev, rctx.RoutePolicy{}).Conversation != rctx.ResolveEnvelope(a, rctx.RoutePolicy{}).Conversation {
				t.Fatal("stop addressed another session")
			}
			f.set(func() { f.events = append(f.events, change{TaskID: "A", Kind: "task.changed"}) })
			if _, text := next(t, p, 2); text != "" {
				t.Fatalf("stop repeated before turn end: %q", text)
			}
		})
	}
}

func TestIdleTaskIsNotStopped(t *testing.T) {
	f := newFake()
	f.set(func() { f.inbox = []pending{{TaskID: "A", NextActor: "assignee"}} })
	p := newTestPlatform(t, f)
	next(t, p, 1)
	f.set(func() { f.inbox, f.status["A"] = nil, "cancelled" })
	f.commit("A", "alice", "cancel", "")
	if _, text := next(t, p, 2); text != "" {
		t.Fatalf("idle session got %q", text)
	}
}

func TestOutageBacksOffAndKeepsCursor(t *testing.T) {
	f := newFake()
	f.set(func() { f.inbox = []pending{{TaskID: "A", NextActor: "assignee"}} })
	p := newTestPlatform(t, f)
	next(t, p, 1)

	f.set(func() { f.down = true })
	f.commit("A", "alice", "comment", "during outage")
	for i := 0; i < 5; i++ {
		if err := p.step(context.Background()); err == nil {
			t.Fatal("expected failure while down")
		} else {
			p.fail(err)
		}
	}
	if p.backoff <= p.poll || p.backoff > p.maxGap {
		t.Fatalf("backoff %v", p.backoff)
	}
	f.set(func() { f.down = false })
	_, text := next(t, p, 1)
	if !strings.Contains(text, "during outage") {
		t.Fatalf("change during outage lost: %q", text)
	}
}

func TestRejectedCursorResynchronizes(t *testing.T) {
	f := newFake()
	f.set(func() { f.inbox = []pending{{TaskID: "A", NextActor: "assignee"}} })
	p := newTestPlatform(t, f)
	next(t, p, 1)
	p.cursor = "not-a-number"
	if err := p.step(context.Background()); err != nil || p.started {
		t.Fatalf("bad cursor: err=%v started=%v", err, p.started)
	}
	if err := p.step(context.Background()); err != nil || !p.started || p.cursor == "not-a-number" {
		t.Fatalf("resync: err=%v started=%v cursor=%q", err, p.started, p.cursor)
	}
}

func TestRejectsHumanCredential(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(viewer{Subject: "alice", Kind: "human"})
	}))
	defer srv.Close()
	_ = f
	cfg := Config{BaseURL: srv.URL}
	cfg.SetDefaults()
	p := newPlatform(cfg, newClient(srv.URL, "Bearer s", time.Second))
	if err := p.step(context.Background()); err == nil || !strings.Contains(err.Error(), "human") {
		t.Fatalf("human credential accepted: %v", err)
	}
}

func TestReceiveStopsWithContext(t *testing.T) {
	f := newFake()
	cfg := Config{BaseURL: "http://127.0.0.1:1"}
	cfg.SetDefaults()
	p := newPlatform(cfg, newClient(cfg.BaseURL, "Bearer s", 100*time.Millisecond))
	_ = f
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := p.Receive(ctx); err == nil {
		t.Fatal("Receive returned without an event or error")
	}
}
