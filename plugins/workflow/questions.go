package workflow

import (
	"context"
	"strings"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/question"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workflow"
)

// QuestionDeps has eight fields: the command lifecycle, the task and its flow
// position, the timeline that holds the text, the question records, the
// executor's progress, the relations that validate an addressee, and the
// change event. Splitting them would only move the same transaction
// coordination elsewhere.
type QuestionDeps struct {
	Commands  command.Handler    `json:"commands"`
	Tasks     task.Reader        `json:"tasks"`
	Flow      flow.Controller    `json:"flow"`
	Commits   commit.Log         `json:"commits"`
	Questions question.Store     `json:"questions"`
	Progress  execution.Progress `json:"progress"`
	Relations az.Relations       `json:"relations"`
	Events    record.EventWriter `json:"events"`
}
type QuestionModule struct{ d QuestionDeps }

func init() { pluginkit.Register("commands/question", NewQuestions) }

func NewQuestions(_ struct{}, d QuestionDeps) (*QuestionModule, error) {
	if d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Commits == nil || d.Questions == nil || d.Progress == nil || d.Relations == nil || d.Events == nil {
		return nil, fault.ErrInput
	}
	return &QuestionModule{d}, nil
}

func validText(body string) bool { return strings.TrimSpace(body) != "" && len(body) <= 20000 }

// activeRound loads the task and requires its current active round.
func (m *QuestionModule) activeRound(ctx context.Context, p identity.Principal, taskID string) (task.Task, flow.Position, error) {
	t, e := m.d.Tasks.GetTask(ctx, p.Tenant, taskID)
	if e != nil {
		return t, flow.Position{}, e
	}
	pos, e := m.d.Flow.Read(ctx, t)
	if e != nil {
		return t, pos, e
	}
	if pos.Status != flow.PhaseActive {
		return t, pos, fault.ErrConflict
	}
	return t, pos, nil
}

func (m *QuestionModule) appendCommit(ctx context.Context, p identity.Principal, t task.Task, kind, body, ref string) (commit.Commit, error) {
	c := commit.Commit{Tenant: t.Tenant, TaskID: t.ID, ID: id(), Kind: kind, Body: body, RefID: ref, Author: p.ID, OwnerID: p.OwnerID, BasisVersion: t.BasisVersion}
	return c, m.d.Commits.Append(ctx, c)
}

// addressee resolves who a question is asked of: the owner by default, or a
// current participant who is an active member. Nobody asks themselves.
func (m *QuestionModule) addressee(ctx context.Context, p identity.Principal, t task.Task, requested string) (string, error) {
	to := strings.TrimSpace(requested)
	if to == "" {
		to = t.Owner
	}
	if to == p.ID {
		return "", fault.ErrInput
	}
	if to == t.Owner {
		return to, nil
	}
	member, e := m.d.Relations.GetMember(ctx, t.Tenant, to)
	if e != nil || !member.Active {
		return "", fault.ErrInput
	}
	if in, e := m.d.Relations.HasParticipant(ctx, az.Participant{Tenant: t.Tenant, TaskID: t.ID, Subject: to}); e != nil || !in {
		return "", fault.ErrInput
	}
	return to, nil
}

// Ask is open to any task participant; it waits on the addressee.
func (m *QuestionModule) Ask(ctx context.Context, token, key string, in api.Ask) (api.Ref, error) {
	return run(ctx, m.d.Commands, token, key, "commit.append", in.TaskID, in, func(tx context.Context, p identity.Principal) (api.Ref, error) {
		if !validText(in.Body) {
			return api.Ref{}, fault.ErrInput
		}
		t, pos, e := m.activeRound(tx, p, in.TaskID)
		if e != nil {
			return api.Ref{}, e
		}
		if pos.RoundID != in.RoundID {
			return api.Ref{}, fault.ErrConflict
		}
		to, e := m.addressee(tx, p, t, in.Addressee)
		if e != nil {
			return api.Ref{}, e
		}
		c, e := m.appendCommit(tx, p, t, "question", in.Body, "")
		if e != nil {
			return api.Ref{}, e
		}
		if e = m.d.Questions.CreateQuestion(tx, question.Question{Tenant: t.Tenant, TaskID: t.ID, ID: id(), RoundID: pos.RoundID, CommitID: c.ID, Addressee: to}); e != nil {
			return api.Ref{}, e
		}
		if e = m.d.Progress.RecordProgress(tx, p, t.Tenant, t.ID); e != nil {
			return api.Ref{}, e
		}
		return finish(tx, m.d.Flow, m.d.Events, t)
	})
}

// Answer is reserved to the addressee and only for the current round.
// The flow is refreshed afterwards, so a completion held by the question can proceed.
func (m *QuestionModule) Answer(ctx context.Context, token, key string, in api.Answer) (api.Ref, error) {
	return run(ctx, m.d.Commands, token, key, "commit.append", in.TaskID, in, func(tx context.Context, p identity.Principal) (api.Ref, error) {
		if !validText(in.Body) {
			return api.Ref{}, fault.ErrInput
		}
		t, pos, e := m.activeRound(tx, p, in.TaskID)
		if e != nil {
			return api.Ref{}, e
		}
		q, e := m.d.Questions.GetQuestion(tx, t.Tenant, t.ID, in.QuestionID)
		if e != nil {
			return api.Ref{}, e
		}
		if p.ID != q.Addressee {
			return api.Ref{}, az.ErrForbidden
		}
		if q.AnswerCommitID != "" || q.RoundID != pos.RoundID {
			return api.Ref{}, fault.ErrConflict
		}
		c, e := m.appendCommit(tx, p, t, "answer", in.Body, q.CommitID)
		if e != nil {
			return api.Ref{}, e
		}
		q.AnswerCommitID = c.ID
		if e = m.d.Questions.AnswerQuestion(tx, q); e != nil {
			return api.Ref{}, e
		}
		return finish(tx, m.d.Flow, m.d.Events, t)
	})
}
