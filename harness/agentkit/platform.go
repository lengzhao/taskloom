// Package harness adapts Taskloom to an existing AgentKit runtime. It is an
// AgentKit platform plugin (platform/taskloom), not a Taskloom server plugin:
// it follows the Agent's authorized change feed and wakes one AgentKit
// session per task when that task is waiting on the Agent. All reads use the
// shared business API with the Agent's own credential; all writes stay with
// the Agent's MCP tools, so business rules, fencing and authorization remain
// in Taskloom.
//
// What the adapter decides (Agent-layer concerns, see AGENTS.md):
//   - wake: a task enters the Agent's inbox, or someone else wrote to it while
//     it is there; the Agent's own commits never wake it (no self-reply);
//   - steer: a wake for a task whose turn is running is folded into that turn
//     by the AgentKit runner;
//   - stop: a running turn is stopped when its task is cancelled, the Agent is
//     replaced as assignee or loses access. Taskloom has already fenced the
//     writes; stopping only saves effort and proves nothing about tools.
package harness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lengzhao/agentkit"
	"github.com/lengzhao/agentkit/runtime/rctx"
)

const platformID = "taskloom"

// Config wires the platform to one Taskloom deployment and one Agent credential.
type Config struct {
	// BaseURL is the Taskloom API root, e.g. http://taskloom:8899/v1.
	BaseURL string `json:"baseUrl"`
	// AuthorizationEnv names the environment variable holding the full
	// Authorization header value ("Bearer <secret>"); the secret never sits in
	// config. Default TASKLOOM_AUTHORIZATION.
	AuthorizationEnv string `json:"authorizationEnv"`
	// PollSeconds is how often the change feed is read. Polling the feed costs
	// one small HTTP request; the model is only woken when a task needs it.
	PollSeconds int `json:"pollSeconds"`
	// RequestTimeoutSeconds bounds each HTTP request. Default 15.
	RequestTimeoutSeconds int `json:"requestTimeoutSeconds"`
	// MaxBackoffSeconds caps the retry delay while Taskloom is unreachable.
	MaxBackoffSeconds int `json:"maxBackoffSeconds"`
}

func (c *Config) SetDefaults() {
	if strings.TrimSpace(c.AuthorizationEnv) == "" {
		c.AuthorizationEnv = "TASKLOOM_AUTHORIZATION"
	}
	if c.PollSeconds == 0 {
		c.PollSeconds = 3
	}
	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = 15
	}
	if c.MaxBackoffSeconds == 0 {
		c.MaxBackoffSeconds = 60
	}
}

func (c *Config) Validate() error {
	if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		return fmt.Errorf("platform/taskloom baseUrl must be an http(s) URL, got %q", c.BaseURL)
	}
	if c.PollSeconds < 1 || c.RequestTimeoutSeconds < 1 || c.MaxBackoffSeconds < c.PollSeconds {
		return fmt.Errorf("platform/taskloom needs pollSeconds >= 1, requestTimeoutSeconds >= 1 and maxBackoffSeconds >= pollSeconds")
	}
	return nil
}

// taskState is what the adapter remembers about one task in this process.
// Nothing is persisted: after a restart every task in the inbox is woken once
// and the Agent continues from read_task.
type taskState struct {
	seen     int64 // last timeline sequence already reported to the Agent
	woken    bool  // woken at least once since this process started
	assignee bool  // the Agent was the assignee when last woken
	busy     bool  // a turn of this task's session is running
	stopping bool  // a stop was requested for the running turn
}

type Platform struct {
	cfg    Config
	api    *client
	poll   time.Duration
	maxGap time.Duration
	sleep  func(context.Context, time.Duration) error

	// Receive-side state, touched only by the runner's Receive loop.
	me      string
	cursor  string
	started bool
	queue   []agentkit.MessageEvent
	backoff time.Duration

	// Shared with Send, which turn goroutines call concurrently.
	mu     sync.Mutex
	tasks  map[string]*taskState
	routes map[agentkit.SessionID]string
}

// New registers platform/taskloom.
func New(cfg Config) (agentkit.Platform, error) {
	cfg.SetDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	auth := strings.TrimSpace(os.Getenv(cfg.AuthorizationEnv))
	if auth == "" {
		return nil, fmt.Errorf("platform/taskloom: environment variable %s is empty", cfg.AuthorizationEnv)
	}
	return newPlatform(cfg, newClient(cfg.BaseURL, auth, time.Duration(cfg.RequestTimeoutSeconds)*time.Second)), nil
}

func newPlatform(cfg Config, api *client) *Platform {
	return &Platform{
		cfg:    cfg,
		api:    api,
		poll:   time.Duration(cfg.PollSeconds) * time.Second,
		maxGap: time.Duration(cfg.MaxBackoffSeconds) * time.Second,
		sleep:  sleepContext,
		tasks:  map[string]*taskState{},
		routes: map[agentkit.SessionID]string{},
	}
}

func (p *Platform) PlatformID() string { return platformID }

// Receive blocks until a task needs the Agent. It owns no goroutine: polling
// happens inline, so cancelling ctx stops it and nothing outlives the runner.
func (p *Platform) Receive(ctx context.Context) (agentkit.MessageEvent, error) {
	for {
		if err := ctx.Err(); err != nil {
			return agentkit.MessageEvent{}, err
		}
		if len(p.queue) > 0 {
			ev := p.queue[0]
			p.queue = p.queue[1:]
			return ev, nil
		}
		wait := p.poll
		if p.backoff > 0 {
			wait = p.backoff
		}
		if p.started {
			if err := p.sleep(ctx, wait); err != nil {
				return agentkit.MessageEvent{}, err
			}
		}
		if err := p.step(ctx); err != nil {
			if ctx.Err() != nil {
				return agentkit.MessageEvent{}, ctx.Err()
			}
			p.fail(err)
			if !p.started {
				if err := p.sleep(ctx, p.backoff); err != nil {
					return agentkit.MessageEvent{}, err
				}
			}
			continue
		}
		p.backoff = 0
	}
}

func (p *Platform) fail(err error) {
	if p.backoff == 0 {
		p.backoff = p.poll
	} else {
		p.backoff = min(2*p.backoff, p.maxGap)
	}
	level := slog.LevelWarn
	if errors.Is(err, errAuth) {
		// A rejected credential needs an operator; keep running so a restored
		// credential store or a restart with a new secret recovers.
		level = slog.LevelError
		p.backoff = p.maxGap
	}
	slog.Log(context.Background(), level, "taskloom poll failed", "err", err, "retry_in", p.backoff.String())
}

func (p *Platform) step(ctx context.Context) error {
	if !p.started {
		return p.start(ctx)
	}
	page, err := p.api.changes(ctx, p.cursor)
	if err != nil {
		if errors.Is(err, errBadCursor) {
			// The cursor does not belong to this database any more (restored
			// or replaced). Resynchronize from the inbox instead of guessing.
			slog.Warn("taskloom cursor rejected, resynchronizing", "err", err)
			p.started = false
			return nil
		}
		return err
	}
	if len(page.Changes) == 0 {
		p.cursor = page.Cursor
		return nil
	}
	ids := map[string]bool{}
	all := false
	for _, c := range page.Changes {
		if c.Kind == "access.changed" {
			all = true
		}
		if c.TaskID != "" {
			ids[c.TaskID] = true
		}
	}
	if err := p.review(ctx, ids, all); err != nil {
		return err // keep the cursor: the same changes are read again
	}
	p.cursor = page.Cursor
	return nil
}

// start takes the cursor before reading the inbox, so no change falls between
// the two reads; a change seen twice only re-evaluates a task.
func (p *Platform) start(ctx context.Context) error {
	v, err := p.api.me(ctx)
	if err != nil {
		return err
	}
	if v.Kind != "" && v.Kind != "agent" {
		return fmt.Errorf("%w: credential belongs to a %s, not an agent", errAuth, v.Kind)
	}
	page, err := p.api.changes(ctx, "")
	if err != nil {
		return err
	}
	p.me = v.Subject
	if err := p.review(ctx, nil, true); err != nil {
		return err
	}
	p.cursor = page.Cursor
	p.started = true
	slog.Info("taskloom platform started", "agent", p.me, "tenant", v.Tenant)
	return nil
}

// review re-evaluates the given tasks (all inbox and running tasks when all is
// set) against the inbox, which Taskloom derives from the same facts as every
// snapshot. The adapter keeps no copy of business rules.
func (p *Platform) review(ctx context.Context, ids map[string]bool, all bool) error {
	inbox, err := p.api.inbox(ctx)
	if err != nil {
		return err
	}
	waiting := map[string]pending{}
	for _, it := range inbox {
		if it.NextActor != "" { // blocked entries have nothing to do yet
			waiting[it.TaskID] = it
		}
	}
	if ids == nil {
		ids = map[string]bool{}
	}
	if all {
		for id := range waiting {
			ids[id] = true
		}
		p.mu.Lock()
		for id, st := range p.tasks {
			if st.busy {
				ids[id] = true
			}
		}
		p.mu.Unlock()
	}
	for id := range ids {
		var err error
		if it, ok := waiting[id]; ok {
			err = p.wakeIfNew(ctx, it)
		} else {
			err = p.stopIfLost(ctx, id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *Platform) state(id string) *taskState {
	st := p.tasks[id]
	if st == nil {
		st = &taskState{}
		p.tasks[id] = st
	}
	return st
}

// wakeIfNew wakes the task's session the first time it is seen waiting, and
// afterwards only when someone other than the Agent wrote to it.
func (p *Platform) wakeIfNew(ctx context.Context, it pending) error {
	p.mu.Lock()
	seen := p.state(it.TaskID).seen
	p.mu.Unlock()
	title, commits, err := p.api.commitsAfter(ctx, it.TaskID, seen)
	if errors.Is(err, errGone) {
		return nil // lost between the inbox read and this one; the next change says more
	}
	if err != nil {
		return err
	}
	var others []commitView
	for _, c := range commits {
		seen = max(seen, c.Sequence)
		if c.Author != p.me {
			others = append(others, c)
		}
	}
	p.mu.Lock()
	st := p.state(it.TaskID)
	first := !st.woken
	st.seen = seen
	if !first && len(others) == 0 {
		p.mu.Unlock()
		return nil
	}
	st.woken = true
	if it.NextActor == "assignee" {
		st.assignee = true // an addressee wake keeps what an earlier wake knew
	}
	ev := p.event(it.TaskID, wakeText(it, title, others, first))
	p.routes[routeKey(ev)] = it.TaskID
	p.mu.Unlock()
	slog.Info("taskloom wake", "task", it.TaskID, "next_actor", it.NextActor, "first", first, "new_commits", len(others))
	p.queue = append(p.queue, ev)
	return nil
}

// stopIfLost stops a running turn whose task the Agent may no longer work on.
// A task merely waiting on someone else (a question to the owner, a result in
// review) is left alone: the turn is finishing on its own.
func (p *Platform) stopIfLost(ctx context.Context, id string) error {
	p.mu.Lock()
	st := p.tasks[id]
	running := st != nil && st.busy && !st.stopping
	wasAssignee := st != nil && st.assignee
	p.mu.Unlock()
	if !running {
		return nil
	}
	reason := ""
	snap, err := p.api.workflow(ctx, id)
	switch {
	case errors.Is(err, errGone):
		reason = "access lost"
	case err != nil:
		return err
	case snap.Position.Status == "cancelled":
		reason = "task cancelled"
	case wasAssignee && snap.Assignee != p.me:
		reason = "assignee replaced"
	}
	if reason == "" {
		return nil
	}
	p.mu.Lock()
	st.stopping = true
	ev := p.event(id, "/stop")
	p.mu.Unlock()
	slog.Info("taskloom stop", "task", id, "reason", reason)
	p.queue = append(p.queue, ev)
	return nil
}

// event addresses one task's session. Each task is its own channel, so its
// history, steering and stop stay apart from every other task.
func (p *Platform) event(taskID, text string) agentkit.MessageEvent {
	ev := agentkit.MessageEvent{Message: agentkit.ModelMessage{
		Role:    "user",
		Content: []agentkit.ContentPart{{Type: "text", Text: text}},
	}}
	return rctx.WithInboundRoute(ev, agentkit.SessionRouteInput{Platform: platformID, Instance: platformID, ChannelID: taskID})
}

func routeKey(ev agentkit.MessageEvent) agentkit.SessionID {
	id, _ := rctx.RouteSessionID(ev.Envelope.Route)
	return id
}

// Send observes turn boundaries to know which task sessions are running. The
// Agent reports to people only through Taskloom tools, so nothing is posted.
func (p *Platform) Send(_ context.Context, out agentkit.OutboundEvent) error {
	if out.Type != agentkit.EventTurnStart && out.Type != agentkit.EventTurnEnd && out.Type != "error" {
		return nil
	}
	key := rctx.OutboundRouteID(out)
	p.mu.Lock()
	id, ok := p.routes[key]
	if ok {
		st := p.state(id)
		switch out.Type {
		case agentkit.EventTurnStart:
			st.busy = true
		case agentkit.EventTurnEnd:
			st.busy, st.stopping = false, false
		}
	}
	p.mu.Unlock()
	switch out.Type {
	case agentkit.EventTurnEnd:
		slog.Info("taskloom turn end", "task", id, "data", truncate(string(out.Data), 300))
	case "error":
		slog.Warn("taskloom turn error", "task", id, "data", truncate(string(out.Data), 500))
	}
	return nil
}

func wakeText(it pending, title string, others []commitView, first bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Taskloom] 任务 %s「%s」现在等你处理（你的角色：%s）。\n", it.TaskID, title, it.NextActor)
	if !first && len(others) > 0 {
		b.WriteString("自你上次被唤醒后，其他人的新动态：\n")
		for _, c := range others {
			fmt.Fprintf(&b, "- %s（%s）：%s\n", c.Author, c.Kind, truncate(strings.TrimSpace(c.Body), 800))
		}
	}
	fmt.Fprintf(&b, "只处理这个任务：先 read_task(task_id=%q) 读取完整情况再按规则推进；处理完或需要等待别人时调用 finish。", it.TaskID)
	return b.String()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
