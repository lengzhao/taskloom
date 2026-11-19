package flow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	capflow "github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/question"
	"github.com/lengzhao/taskloom/cap/task"
)

type Config struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}
type Deps struct {
	Definitions capflow.DefinitionStore `json:"definitions"`
	Positions   capflow.PositionStore   `json:"positions"`
	States      []capflow.State         `json:"states"`
	Executions  execution.Reader        `json:"executions,omitempty"`
	// Questions holds completion while a request for input of the round is open.
	Questions question.Reader `json:"questions,omitempty"`
}
type Controller struct {
	c   Config
	d   Deps
	ref capflow.DefinitionRef
}

func init() { pluginkit.Register("task/flow", New); pluginkit.Register("flow/directory", NewDirectory) }
func New(c Config, d Deps) (*Controller, error) {
	if c.ID == "" || c.Version == "" || d.Definitions == nil || d.Positions == nil || len(d.States) == 0 {
		return nil, errors.New("flow definition, states and persistence required")
	}
	seen := map[string]bool{}
	descs := []capflow.Description{}
	terminal := false
	for i, s := range d.States {
		if s == nil {
			return nil, fault.ErrInput
		}
		v := s.Describe()
		if v.Key == "" || v.Version == "" || seen[v.Key] {
			return nil, errors.New("invalid state descriptions")
		}
		seen[v.Key] = true
		if v.Role == capflow.RoleCompleted {
			if i != len(d.States)-1 {
				return nil, errors.New("completed state must be last")
			}
			terminal = true
		}
		descs = append(descs, v)
	}
	if len(d.States) > 1 && (descs[0].Role != capflow.RoleReady || !terminal || d.Executions == nil || d.Questions == nil) {
		return nil, errors.New("workflow requires ready, terminal, execution safety and open-question gate")
	}
	var b []byte
	if len(descs) == 1 {
		b, _ = json.Marshal(struct{ Key, Version string }{descs[0].Key, descs[0].Version})
	} else {
		b, _ = json.Marshal(descs)
	}
	return &Controller{c: c, d: d, ref: capflow.DefinitionRef{ID: c.ID, Version: c.Version, Digest: fmt.Sprintf("%x", sha256.Sum256(b))}}, nil
}
func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return fmt.Sprintf("%x", b)
}
func (c *Controller) Reference() capflow.DefinitionRef { return c.ref }
func (c *Controller) stateContext(t task.Task, p capflow.Position, i int) capflow.Context {
	p.EntryID = p.Entries[i].ID
	p.State = p.Entries[i].Key
	return capflow.Context{Task: t, Position: p}
}

// held returns the fixed, flow-independent reason a task may not complete
// yet: an unanswered request for input in the current round. Completion does
// not wait for executions to stop; an accepted result stands on its own and a
// terminal task fences any further writes.
func (c *Controller) held(ctx context.Context, t task.Task, p capflow.Position) (string, error) {
	if c.d.Questions == nil {
		return "", nil
	}
	open, e := c.d.Questions.OpenQuestions(ctx, t.Tenant, t.ID, p.RoundID)
	if e != nil {
		return "", e
	}
	if len(open) > 0 {
		return "等待负责人回答问题", nil
	}
	return "", nil
}
func (c *Controller) Initialize(ctx context.Context, t task.Task) (capflow.Position, error) {
	p := capflow.Position{Tenant: t.Tenant, TaskID: t.ID, RoundID: id(), EntryID: id(), State: c.d.States[0].Describe().Key, Flow: c.ref, Status: capflow.PhaseActive}
	p.Entries = []capflow.Entry{{Key: p.State, ID: p.EntryID}}
	if e := c.d.Definitions.EnsureDefinition(ctx, c.ref); e != nil {
		return p, e
	}
	entry, e := c.d.States[0].CheckEnter(ctx, c.stateContext(t, p, 0))
	if e != nil {
		return p, e
	}
	if entry.Status != capflow.Allow {
		return p, fault.ErrConflict
	}
	if e = c.d.Positions.CreatePosition(ctx, p); e != nil {
		return p, e
	}
	return c.Refresh(ctx, t)
}
func (c *Controller) Read(ctx context.Context, tenant, taskID string) (capflow.Position, error) {
	p, e := c.d.Positions.GetPosition(ctx, tenant, taskID)
	if e != nil {
		return p, e
	}
	if p.Flow != c.ref {
		return p, fault.ErrConflict
	}
	if len(p.Entries) == 0 {
		p.Entries = []capflow.Entry{{Key: p.State, ID: p.EntryID}}
	}
	if p.Status == "" {
		p.Status = capflow.PhaseActive
	}
	if p.Index < 0 || p.Index >= len(c.d.States) || len(p.Entries) != p.Index+1 {
		return p, fault.ErrConflict
	}
	return p, nil
}
func validate(check capflow.Check) error {
	switch check.Status {
	case capflow.Allow:
		return nil
	case capflow.Wait, capflow.Reject:
		if check.Reason != "" {
			return nil
		}
	}
	return errors.New("invalid state check result")
}
func (c *Controller) Refresh(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.Read(ctx, t.Tenant, t.ID)
	if e != nil {
		return p, e
	}
	if p.Status == capflow.PhaseCompleted {
		return p, nil
	}
	before := p
	before.Entries = append([]capflow.Entry(nil), p.Entries...)
	if p.Status == capflow.PhaseCancelled {
		p.Waiting = "任务已取消"
		return c.save(ctx, before, p)
	}
	for iteration := 0; iteration < len(c.d.States)+1; iteration++ {
		// Synchronize every reached step before judging any of them, so a new
		// candidate cannot retain an old approval while an earlier audit is waiting.
		for i := 0; i <= p.Index; i++ {
			if e = c.d.States[i].Enter(ctx, c.stateContext(t, p, i)); e != nil {
				return p, e
			}
		}
		p.Complete = true
		p.Waiting = ""
		for i := 0; i <= p.Index; i++ {
			check, e := c.d.States[i].CheckComplete(ctx, c.stateContext(t, p, i))
			if e != nil {
				return p, e
			}
			if e = validate(check); e != nil {
				return p, e
			}
			if check.Status != capflow.Allow {
				p.Complete = false
				p.Waiting = check.Reason
				if i < p.Index {
					p.Waiting = c.d.States[i].Describe().Key + ": " + check.Reason
				}
				break
			}
		}
		if !p.Complete {
			break
		}
		if p.Index == len(c.d.States)-1 {
			if c.d.States[p.Index].Describe().Role == capflow.RoleCompleted {
				held, e := c.held(ctx, t, p)
				if e != nil {
					return p, e
				}
				if held != "" {
					p.Complete = false
					p.Waiting = held
					break
				}
				p.Status = capflow.PhaseCompleted
			}
			break
		}
		next := p.Index + 1
		proposed := p
		proposed.Entries = append(append([]capflow.Entry{}, p.Entries...), capflow.Entry{Key: c.d.States[next].Describe().Key, ID: id()})
		check, e := c.d.States[next].CheckEnter(ctx, c.stateContext(t, proposed, next))
		if e != nil {
			return p, e
		}
		if e = validate(check); e != nil {
			return p, e
		}
		if check.Status != capflow.Allow {
			p.Waiting = check.Reason
			break
		}
		if c.d.States[next].Describe().Role == capflow.RoleCompleted {
			held, e := c.held(ctx, t, p)
			if e != nil {
				return p, e
			}
			if held != "" {
				p.Waiting = held
				break
			}
		}
		p.Entries = proposed.Entries
		p.Index = next
		p.State = p.Entries[next].Key
		p.EntryID = p.Entries[next].ID
	}
	return c.save(ctx, before, p)
}

// save persists p only when it differs from before. A no-op refresh (e.g. after
// a lease renewal) keeps the revision, so clients' expected versions stay valid.
func (c *Controller) save(ctx context.Context, before, p capflow.Position) (capflow.Position, error) {
	if reflect.DeepEqual(before, p) {
		return before, nil
	}
	p.Revision = before.Revision + 1
	if e := c.d.Positions.SavePosition(ctx, p, before.Revision); e != nil {
		return before, e
	}
	return p, nil
}

func (c *Controller) Step(ctx context.Context, t task.Task, key string) (capflow.Context, error) {
	p, e := c.Read(ctx, t.Tenant, t.ID)
	if e != nil {
		return capflow.Context{}, e
	}
	if p.Status != capflow.PhaseActive {
		return capflow.Context{}, fault.ErrConflict
	}
	for i := range p.Entries {
		if p.Entries[i].Key == key {
			return c.stateContext(t, p, i), nil
		}
	}
	return capflow.Context{}, fault.ErrConflict
}
func (c *Controller) CheckOpen(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.Read(ctx, t.Tenant, t.ID)
	if e != nil {
		return p, e
	}
	if p.Status != capflow.PhaseActive || c.d.States[p.Index].Describe().Role != capflow.RoleExecuting {
		return p, fault.ErrConflict
	}
	for i := 0; i < p.Index; i++ {
		check, e := c.d.States[i].CheckComplete(ctx, c.stateContext(t, p, i))
		if e != nil {
			return p, e
		}
		if check.Status != capflow.Allow {
			return p, fault.ErrConflict
		}
	}
	check, e := c.d.States[p.Index].CheckEnter(ctx, c.stateContext(t, p, p.Index))
	if e != nil {
		return p, e
	}
	if check.Status != capflow.Allow {
		return p, fault.ErrConflict
	}
	return p, nil
}

// CheckEdit allows changing the task basis before work on the current round
// has been claimed: in the first step, or while executing with no live claim
// in this round. A running claim must not see its criteria change silently.
func (c *Controller) CheckEdit(ctx context.Context, t task.Task) error {
	p, e := c.Read(ctx, t.Tenant, t.ID)
	if e != nil {
		return e
	}
	if p.Status != capflow.PhaseActive {
		return fault.ErrConflict
	}
	role := c.d.States[p.Index].Describe().Role
	if role != capflow.RoleReady && role != capflow.RoleExecuting {
		return fault.ErrConflict
	}
	if c.d.Executions == nil {
		return nil
	}
	current, e := c.d.Executions.Current(ctx, t.Tenant, t.ID)
	if errors.Is(e, fault.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	if current.Writable && current.RoundID == p.RoundID {
		return fault.ErrConflict
	}
	return nil
}
func (c *Controller) Cancel(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.Read(ctx, t.Tenant, t.ID)
	if e != nil {
		return p, e
	}
	if p.Status != capflow.PhaseActive {
		return p, fault.ErrConflict
	}
	before := p
	p.Status = capflow.PhaseCancelled
	p.Complete = false
	p.Waiting = "任务已取消"
	return c.save(ctx, before, p)
}
func (c *Controller) restart(ctx context.Context, t task.Task, reopen bool) (capflow.Position, error) {
	p, e := c.Read(ctx, t.Tenant, t.ID)
	if e != nil {
		return p, e
	}
	if reopen == (p.Status == capflow.PhaseActive) {
		return p, fault.ErrConflict
	}
	before := p
	before.Entries = append([]capflow.Entry(nil), p.Entries...)
	p.RoundID = id()
	p.EntryID = id()
	p.State = c.d.States[0].Describe().Key
	p.Index = 0
	p.Entries = []capflow.Entry{{Key: p.State, ID: p.EntryID}}
	p.Status = capflow.PhaseActive
	p.Complete = false
	p.Waiting = ""
	if p, e = c.save(ctx, before, p); e != nil {
		return p, e
	}
	return c.Refresh(ctx, t)
}
func (c *Controller) Return(ctx context.Context, t task.Task) (capflow.Position, error) {
	return c.restart(ctx, t, false)
}
func (c *Controller) Reopen(ctx context.Context, t task.Task) (capflow.Position, error) {
	return c.restart(ctx, t, true)
}

type DirectoryConfig struct {
	DefaultID      string `json:"defaultID"`
	DefaultVersion string `json:"defaultVersion"`
}
type DirectoryDeps struct {
	Positions capflow.PositionReader `json:"positions"`
	Flows     []capflow.Definition   `json:"flows"`
}
type Directory struct {
	c  DirectoryConfig
	d  DirectoryDeps
	by map[string]capflow.Definition
}

func NewDirectory(c DirectoryConfig, d DirectoryDeps) (*Directory, error) {
	v := &Directory{c: c, d: d, by: map[string]capflow.Definition{}}
	if d.Positions == nil {
		return nil, fault.ErrInput
	}
	for _, f := range d.Flows {
		r := f.Reference()
		key := r.ID + "/" + r.Version
		if v.by[key] != nil {
			return nil, fault.ErrConflict
		}
		v.by[key] = f
	}
	if v.by[c.DefaultID+"/"+c.DefaultVersion] == nil {
		return nil, fault.ErrNotFound
	}
	return v, nil
}
func (d *Directory) bound(ctx context.Context, t task.Task) (capflow.Definition, error) {
	p, e := d.d.Positions.GetPosition(ctx, t.Tenant, t.ID)
	if e != nil {
		return nil, e
	}
	f := d.by[p.Flow.ID+"/"+p.Flow.Version]
	if f == nil || f.Reference() != p.Flow {
		return nil, fault.ErrConflict
	}
	return f, nil
}
func (d *Directory) Initialize(ctx context.Context, t task.Task) (capflow.Position, error) {
	return d.by[d.c.DefaultID+"/"+d.c.DefaultVersion].Initialize(ctx, t)
}
func (d *Directory) Read(ctx context.Context, tenant, id string) (capflow.Position, error) {
	t := task.Task{Tenant: tenant, ID: id}
	f, e := d.bound(ctx, t)
	if e != nil {
		return capflow.Position{}, e
	}
	return f.Read(ctx, tenant, id)
}

func (d *Directory) Refresh(ctx context.Context, t task.Task) (capflow.Position, error) {
	f, e := d.bound(ctx, t)
	if e != nil {
		return capflow.Position{}, e
	}
	return f.Refresh(ctx, t)
}

func (d *Directory) CheckOpen(ctx context.Context, t task.Task) (capflow.Position, error) {
	f, e := d.bound(ctx, t)
	if e != nil {
		return capflow.Position{}, e
	}
	return f.CheckOpen(ctx, t)
}

func (d *Directory) Cancel(ctx context.Context, t task.Task) (capflow.Position, error) {
	f, e := d.bound(ctx, t)
	if e != nil {
		return capflow.Position{}, e
	}
	return f.Cancel(ctx, t)
}

func (d *Directory) Return(ctx context.Context, t task.Task) (capflow.Position, error) {
	f, e := d.bound(ctx, t)
	if e != nil {
		return capflow.Position{}, e
	}
	return f.Return(ctx, t)
}

func (d *Directory) Reopen(ctx context.Context, t task.Task) (capflow.Position, error) {
	f, e := d.bound(ctx, t)
	if e != nil {
		return capflow.Position{}, e
	}
	return f.Reopen(ctx, t)
}

func (d *Directory) CheckEdit(ctx context.Context, t task.Task) error {
	f, e := d.bound(ctx, t)
	if e != nil {
		return e
	}
	return f.CheckEdit(ctx, t)
}

func (d *Directory) Step(ctx context.Context, t task.Task, key string) (capflow.Context, error) {
	f, e := d.bound(ctx, t)
	if e != nil {
		return capflow.Context{}, e
	}
	return f.Step(ctx, t, key)
}
