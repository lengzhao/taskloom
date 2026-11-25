// Package ownership hands a task over to another responsible person. The
// owner is who reviews results and manages the task, so a handover changes
// those rights and nothing else: assignee, executions and basis stay.
package ownership

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/runtime/command"
)

// Deps has eight fields, reviewed: each is one fact written or checked in the
// same handover transaction (owner, the new owner's membership, the old
// owner's participation, the timeline commit, completion under the new
// reviewer, the change event) plus the command lifecycle and its own
// authorization, which lets the former owner replay a handover they made.
// Splitting them would only move this one transaction elsewhere.
type Deps struct {
	Commands      capcommand.Handler   `json:"commands"`
	Authorization az.Authorizer        `json:"authorization"`
	Tasks         task.OwnerSetter     `json:"tasks"`
	Members       az.MemberDirectory   `json:"members"`
	Participants  az.ParticipantWriter `json:"participants"`
	Commits       commit.Log           `json:"commits"`
	// Flow re-derives completion: whether a verdict counts depends on who
	// the owner is, and the parent learns if this task completed.
	Flow   flow.Controller    `json:"flow"`
	Events record.EventWriter `json:"events"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("commands/ownership", func(_ struct{}, d Deps) (*Module, error) {
		if d.Commands == nil || d.Authorization == nil || d.Tasks == nil || d.Members == nil || d.Participants == nil || d.Commits == nil || d.Flow == nil || d.Events == nil {
			return nil, fault.ErrInput
		}
		return &Module{d}, nil
	})
}

const maxReason = 20000

// Handover implements api.Ownership. Only the current owner or an
// administrator may hand over (task.owner); the new owner must be an active
// person. The old owner stays a participant and keeps reading the task.
func (m *Module) Handover(ctx context.Context, token, key string, in api.Handover) (api.Ref, error) {
	in.Owner, in.SeenOwner = strings.TrimSpace(in.Owner), strings.TrimSpace(in.SeenOwner)
	if in.Owner == "" || in.SeenOwner == "" || len(in.Reason) > maxReason {
		return api.Ref{}, fault.ErrInput
	}
	req := capcommand.Request{Token: token, Key: key, Operation: "task.owner", Resource: in.TaskID, Authorize: func(tx context.Context, p identity.Principal) error {
		e := m.d.Authorization.Check(tx, p, "task.owner", in.TaskID)
		if !errors.Is(e, az.ErrForbidden) || p.ID != in.SeenOwner {
			return e
		}
		// A handover takes task.owner away from the owner who made it, so a
		// retry of it would be refused although it applied. The former owner
		// who still reads the task and sees exactly this handover in effect
		// passes; a new request then fails on SeenOwner and writes nothing.
		if m.d.Authorization.Check(tx, p, "task.read", in.TaskID) != nil {
			return e
		}
		t, err := m.d.Tasks.GetTask(tx, p.Tenant, in.TaskID)
		if err != nil || t.Owner != in.Owner {
			return e
		}
		return nil
	}}
	b, e := command.Run(ctx, m.d.Commands, req, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, e := m.d.Tasks.GetTask(tx, p.Tenant, in.TaskID)
		if e != nil {
			return nil, e
		}
		if t.Owner != in.SeenOwner {
			return nil, fault.Conflict("owner_changed")
		}
		if in.Owner == t.Owner {
			return nil, fault.ErrInput
		}
		if e = m.person(tx, t.Tenant, in.Owner); e != nil {
			return nil, e
		}
		before, e := m.d.Flow.Read(tx, t)
		if e != nil {
			return nil, e
		}
		old := t.Owner
		if t, e = m.d.Tasks.SetOwner(tx, t.Tenant, t.ID, in.Owner); e != nil {
			return nil, e
		}
		if e = m.d.Participants.SetParticipant(tx, az.Participant{Tenant: t.Tenant, TaskID: t.ID, Subject: old}, true); e != nil {
			return nil, e
		}
		body, e := json.Marshal(api.OwnerChange{From: old, To: in.Owner, Reason: in.Reason})
		if e != nil {
			return nil, e
		}
		c := commit.Commit{Tenant: t.Tenant, TaskID: t.ID, ID: id(), Kind: api.KindOwnerChanged, Body: string(body), Author: p.ID, OwnerID: p.OwnerID, BasisVersion: t.BasisVersion}
		if e = m.d.Commits.Append(tx, c); e != nil {
			return nil, e
		}
		after, e := m.d.Flow.Refresh(tx, t)
		if e != nil {
			return nil, e
		}
		// Nobody loses reading (the old owner stays a participant) and the new
		// owner can read once the change lands, so the task change reaches
		// everyone whose rights moved.
		if e = m.d.Events.AppendEvent(tx, record.Event{Tenant: t.Tenant, Resource: t.ID, Kind: record.EventTaskChanged}); e != nil {
			return nil, e
		}
		if after.Status != before.Status {
			parent, e := m.d.Flow.Parent(tx, t)
			if e != nil {
				return nil, e
			}
			if parent != "" {
				if e = m.d.Events.AppendEvent(tx, record.Event{Tenant: t.Tenant, Resource: parent, Kind: record.EventTaskChanged}); e != nil {
					return nil, e
				}
			}
		}
		return json.Marshal(api.Ref{TaskID: t.ID, CommitID: c.ID})
	})
	var out api.Ref
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}

// person accepts an active human member of the tenant.
func (m *Module) person(ctx context.Context, tenant, subject string) error {
	people, e := m.d.Members.ListActiveMembers(ctx, tenant, az.MemberQuery{Subject: subject, Kind: "human"})
	if e != nil {
		return e
	}
	if len(people) == 1 {
		return nil
	}
	return fault.ErrInput
}

func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return fmt.Sprintf("%x", b)
}
