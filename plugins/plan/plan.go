// Package plan implements placing a task in the work breakdown: attaching,
// toggling Required and moving a subtask in one transaction.
package plan

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	capplan "github.com/lengzhao/taskloom/cap/plan"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	"github.com/lengzhao/taskloom/runtime/command"
)

// Deps has seven fields, reviewed: the command lifecycle, plus checks on each
// task touched (authorization, flow phase) and the facts one placement writes
// together (relation, parent basis, parent commit, change events). Splitting
// any of them out would break the single transaction a placement needs.
type Deps struct {
	Commands      capcommand.Handler `json:"commands"`
	Authorization az.Authorizer      `json:"authorization"`
	Tasks         task.BasisAdvancer `json:"tasks"`
	Flow          flow.Controller    `json:"flow"`
	Plan          capplan.Store      `json:"plan"`
	Commits       commit.Log         `json:"commits"`
	Events        record.EventWriter `json:"events"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("commands/plan", func(_ struct{}, d Deps) (*Module, error) { return New(d) })
}

func New(d Deps) (*Module, error) {
	if d.Commands == nil || d.Authorization == nil || d.Tasks == nil || d.Flow == nil || d.Plan == nil || d.Commits == nil || d.Events == nil {
		return nil, errors.New("plan command dependencies required")
	}
	return &Module{d}, nil
}

func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return fmt.Sprintf("%x", b)
}

// side is one end of a change, as recorded in the parent's commit.
type side struct {
	ParentID string `json:"parent"`
	Required bool   `json:"required"`
}
type change struct {
	ChildID string `json:"child"`
	From    side   `json:"from"`
	To      side   `json:"to"`
}

// authorize checks planning rights on every parent touched and, unless only
// Required changes, on the child too. It runs before writes and replays.
func (m *Module) authorize(in capplan.Placement) func(context.Context, identity.Principal) error {
	return func(ctx context.Context, p identity.Principal) error {
		targets := []string{}
		if in.ParentID != in.SeenParentID {
			targets = append(targets, in.ChildID)
		}
		for _, id := range []string{in.SeenParentID, in.ParentID} {
			if id != "" {
				targets = append(targets, id)
			}
		}
		for _, id := range targets {
			if e := m.d.Authorization.Check(ctx, p, "task.plan", id); e != nil {
				return e
			}
		}
		return nil
	}
}

func valid(in capplan.Placement) bool {
	return in.ChildID != "" && in.ParentID != "" && in.ChildID != in.ParentID
}

func (m *Module) Place(ctx context.Context, token, key string, in capplan.Placement) error {
	if !valid(in) {
		return fault.ErrInput
	}
	req := capcommand.Request{Token: token, Key: key, Operation: "task.plan", Resource: in.ChildID, Authorize: m.authorize(in)}
	_, e := command.Run(ctx, m.d.Commands, req, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		return nil, m.Apply(tx, p, in)
	})
	return e
}

// Apply is the placement rule itself, run inside the caller's command
// transaction by Place or by task creation under a parent. The caller has
// authorized the actor; Apply checks the relation facts and writes the
// relation, parent basis and commits, and the change events.
func (m *Module) Apply(ctx context.Context, p identity.Principal, in capplan.Placement) error {
	if !valid(in) {
		return fault.ErrInput
	}
	child, e := m.d.Tasks.GetTask(ctx, p.Tenant, in.ChildID)
	if e != nil {
		return e
	}
	var from side
	current, e := m.d.Plan.Parent(ctx, p.Tenant, child.ID)
	if e == nil {
		from = side{ParentID: current.ParentID, Required: current.Required}
	} else if !errors.Is(e, fault.ErrNotFound) {
		return e
	}
	// The caller must have seen the relation it changes.
	if from.ParentID != in.SeenParentID || (from.ParentID != "" && from.Required != in.SeenRequired) {
		return fault.ErrConflict
	}
	to := side{ParentID: in.ParentID, Required: in.Required}
	if from == to {
		return nil // nothing changes, so no version advances
	}
	// Projects only organize tasks and grant nothing, so parent and child may
	// sit in different projects. A child must be newer than its parent: along
	// any parent chain creation order strictly decreases, so no chain can
	// return to where it started and the tree stays acyclic without a walk.
	parent, e := m.d.Tasks.GetTask(ctx, p.Tenant, to.ParentID)
	if e != nil {
		return e
	}
	if child.Sequence <= parent.Sequence {
		return fault.ErrConflict
	}
	// Every parent whose plan changes must be active; a terminal parent's plan is frozen.
	parents := []string{from.ParentID}
	if to.ParentID != from.ParentID {
		parents = append(parents, to.ParentID)
	}
	for _, id := range parents {
		if id == "" {
			continue
		}
		t, e := m.d.Tasks.GetTask(ctx, p.Tenant, id)
		if e != nil {
			return e
		}
		pos, e := m.d.Flow.Read(ctx, t)
		if e != nil {
			return e
		}
		if pos.Status != flow.PhaseActive {
			return fault.ErrConflict
		}
	}
	if e = m.d.Plan.SetParent(ctx, capplan.Link{Tenant: p.Tenant, ParentID: to.ParentID, ChildID: child.ID, Required: to.Required}); e != nil {
		return e
	}
	body, e := json.Marshal(change{ChildID: child.ID, From: from, To: to})
	if e != nil {
		return e
	}
	for _, id := range parents {
		if id == "" {
			continue
		}
		if e = m.record(ctx, p, id, required(id, from, to), string(body)); e != nil {
			return e
		}
	}
	return m.announce(ctx, p.Tenant, child.ID, from.ParentID, to.ParentID)
}

// required reports whether the required set of parent id changed.
func required(id string, from, to side) bool {
	before := from.ParentID == id && from.Required
	after := to.ParentID == id && to.Required
	return before != after
}

// record advances the parent's basis when its required set changed, so
// executions, results and reviews on the old plan stop counting, then appends
// the change to the parent's timeline under the resulting version.
func (m *Module) record(ctx context.Context, p identity.Principal, parentID string, advance bool, body string) error {
	var t task.Task
	var e error
	if advance {
		t, e = m.d.Tasks.AdvanceBasis(ctx, p.Tenant, parentID)
	} else {
		t, e = m.d.Tasks.GetTask(ctx, p.Tenant, parentID)
	}
	if e != nil {
		return e
	}
	c := commit.Commit{Tenant: t.Tenant, TaskID: t.ID, ID: id(), Kind: capplan.KindChanged, Body: body, Author: p.ID, OwnerID: p.OwnerID, BasisVersion: t.BasisVersion}
	if e = m.d.Commits.Append(ctx, c); e != nil {
		return e
	}
	_, e = m.d.Flow.Refresh(ctx, t)
	return e
}

// announce notifies every task whose view changed, once each.
func (m *Module) announce(ctx context.Context, tenant string, ids ...string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if e := m.d.Events.AppendEvent(ctx, record.Event{Tenant: tenant, Resource: id, Kind: record.EventTaskChanged}); e != nil {
			return e
		}
	}
	return nil
}
