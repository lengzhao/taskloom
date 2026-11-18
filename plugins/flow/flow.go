package flow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lengzhao/pluginkit"
	capflow "github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/task"
)

type Config struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}
type Deps struct {
	Definitions capflow.DefinitionStore `json:"definitions"`
	Positions   capflow.PositionStore   `json:"positions"`
	State       capflow.State           `json:"state"`
}
type Controller struct {
	c Config
	d Deps
}

func init() { pluginkit.Register("task/flow", New) }
func New(c Config, d Deps) (*Controller, error) {
	if c.ID == "" || c.Version == "" || d.Definitions == nil || d.Positions == nil || d.State == nil {
		return nil, errors.New("flow definition and dependencies required")
	}
	description := d.State.Describe()
	if description.Key == "" || description.Version == "" {
		return nil, errors.New("state description requires key and version")
	}
	return &Controller{c, d}, nil
}
func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return fmt.Sprintf("%x", b)
}
func (c *Controller) Initialize(ctx context.Context, t task.Task) (capflow.Position, error) {
	p := capflow.Position{Tenant: t.Tenant, TaskID: t.ID, RoundID: id(), EntryID: id(), State: c.d.State.Describe().Key, Flow: capflow.DefinitionRef{ID: c.c.ID, Version: c.c.Version, Digest: fmt.Sprintf("%x", sha256.Sum256(descriptionBytes(c.d.State.Describe())))}}
	if e := c.d.Definitions.EnsureDefinition(ctx, p.Flow); e != nil {
		return p, e
	}
	entry, e := c.d.State.CheckEnter(ctx, t)
	if e != nil {
		return p, e
	}
	if entry.Status != capflow.Allow {
		return p, errors.New("state entry not allowed")
	}
	if e := c.d.State.Enter(ctx, t); e != nil {
		return p, e
	}
	why, e := c.d.State.CheckComplete(ctx, t)
	if e != nil {
		return p, e
	}
	if e = applyCheck(&p, why); e != nil {
		return p, e
	}
	return p, c.d.Positions.CreatePosition(ctx, p)
}
func (c *Controller) Refresh(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.d.Positions.GetPosition(ctx, t.Tenant, t.ID)
	if e != nil {
		return p, e
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(descriptionBytes(c.d.State.Describe())))
	if p.Flow.ID != c.c.ID || p.Flow.Version != c.c.Version || p.Flow.Digest != digest {
		return p, errors.New("unsupported flow definition")
	}
	if e = c.d.State.Enter(ctx, t); e != nil {
		return p, e
	}
	check, e := c.d.State.CheckComplete(ctx, t)
	if e != nil {
		return p, e
	}
	if e = applyCheck(&p, check); e != nil {
		return p, e
	}
	return p, c.d.Positions.SavePosition(ctx, p)
}
func (c *Controller) Read(ctx context.Context, tenant, id string) (capflow.Position, error) {
	p, e := c.d.Positions.GetPosition(ctx, tenant, id)
	if e != nil {
		return p, e
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(descriptionBytes(c.d.State.Describe())))
	if p.Flow.ID != c.c.ID || p.Flow.Version != c.c.Version || p.Flow.Digest != digest {
		return p, errors.New("unsupported flow definition")
	}
	return p, nil
}

func descriptionBytes(d capflow.Description) []byte { b, _ := json.Marshal(d); return b }
func applyCheck(p *capflow.Position, c capflow.Check) error {
	switch c.Status {
	case capflow.Allow:
		p.Complete = true
		p.Waiting = ""
	case capflow.Wait, capflow.Reject:
		if c.Reason == "" {
			return errors.New("blocked state requires a reason")
		}
		p.Complete = false
		p.Waiting = c.Reason
	default:
		return errors.New("invalid state check status")
	}
	return nil
}
