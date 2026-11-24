// Package review appends independent trust reviews of exact skill releases.
// A review only feeds the delivery decision; it never changes visibility or
// content, and a publisher never reviews its own release.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/skills"
	"github.com/lengzhao/taskloom/runtime/command"
)

const maxReason = 2000

type Deps struct {
	Commands capcommand.Handler     `json:"commands"`
	Identity identity.Authenticator `json:"identity"`
	Reviews  skills.Reviews         `json:"reviews"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("commands/skill-review", func(_ struct{}, d Deps) (*Module, error) { return New(d) })
}
func New(d Deps) (*Module, error) {
	if d.Commands == nil || d.Identity == nil || d.Reviews == nil {
		return nil, errors.New("skill review capabilities required")
	}
	return &Module{d}, nil
}

func (m *Module) Review(ctx context.Context, token, key, ref, verdict, reason string) (skills.Review, error) {
	if ref == "" || len(ref) > 128 || (verdict != skills.Approved && verdict != skills.Rejected) || utf8.RuneCountInString(reason) > maxReason {
		return skills.Review{}, fault.ErrInput
	}
	// The tenant is the authorization resource, so authenticate first; the
	// command authenticates again and rechecks the administrator relation.
	caller, err := m.d.Identity.Authenticate(ctx, token)
	if err != nil {
		return skills.Review{}, err
	}
	in := struct{ Ref, Verdict, Reason string }{ref, verdict, reason}
	// The receipt holds the review this key wrote, so a replay returns it and
	// never a later review by someone else.
	out, err := command.Run(ctx, m.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "skill.review", Resource: caller.Tenant}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		r, e := m.d.Reviews.GetSkillRelease(tx, p.Tenant, ref)
		if e != nil {
			return nil, e
		}
		if r.Publisher == p.ID {
			return nil, az.ErrForbidden // independence: never one's own release
		}
		v := skills.Review{Tenant: p.Tenant, Ref: ref, Verdict: verdict, Reason: reason, Reviewer: p.ID, CreatedAt: time.Now().UTC()}
		if e = m.d.Reviews.AppendSkillReview(tx, v); e != nil {
			return nil, e
		}
		return json.Marshal(v)
	})
	if err != nil {
		return skills.Review{}, err
	}
	var v skills.Review
	err = json.Unmarshal(out, &v)
	return v, err
}
