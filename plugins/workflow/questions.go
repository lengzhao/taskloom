package workflow

import (
	"context"
	"strings"
	"time"

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
// executor's progress lease, and the snapshot/event pair every task command
// returns and publishes. Splitting them would only move the same transaction
// coordination elsewhere.
type QuestionDeps struct {
	Commands  command.Handler    `json:"commands"`
	Tasks     task.Reader        `json:"tasks"`
	Flow      flow.Controller    `json:"flow"`
	Commits   commit.Log         `json:"commits"`
	Questions question.Store     `json:"questions"`
	Progress  execution.Progress `json:"progress"`
	Snapshots api.FactsReader    `json:"snapshots"`
	Events    record.EventWriter `json:"events"`
}
type QuestionModule struct {
	s *Service
	q question.Store
	p execution.Progress
}

func init() { pluginkit.Register("commands/question", NewQuestions) }

func NewQuestions(_ struct{}, d QuestionDeps) (*QuestionModule, error) {
	if d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Commits == nil || d.Questions == nil || d.Progress == nil || d.Snapshots == nil || d.Events == nil {
		return nil, fault.ErrInput
	}
	return &QuestionModule{&Service{d: dependencies{Commands: d.Commands, Tasks: d.Tasks, Flow: d.Flow, Commits: d.Commits, Snapshots: d.Snapshots, Events: d.Events}}, d.Questions, d.Progress}, nil
}

func validText(body string) bool { return strings.TrimSpace(body) != "" && len(body) <= 20000 }

// activeRound loads the task and requires its current active round.
func (m *QuestionModule) activeRound(ctx context.Context, p identity.Principal, taskID string) (task.Task, flow.Position, error) {
	t, e := m.s.d.Tasks.GetTask(ctx, p.Tenant, taskID)
	if e != nil {
		return t, flow.Position{}, e
	}
	pos, e := m.s.d.Flow.Read(ctx, t.Tenant, t.ID)
	if e != nil {
		return t, pos, e
	}
	if pos.Status != flow.PhaseActive {
		return t, pos, fault.ErrConflict
	}
	return t, pos, nil
}

func (m *QuestionModule) appendCommit(ctx context.Context, p identity.Principal, t task.Task, kind, body, ref string) (commit.Commit, error) {
	c := commit.Commit{Tenant: t.Tenant, TaskID: t.ID, ID: id(), Kind: kind, Body: body, RefID: ref, Author: p.ID, OwnerID: p.OwnerID, InstanceID: p.InstanceID, Incarnation: p.Incarnation, BasisVersion: t.BasisVersion}
	return c, m.s.d.Commits.Append(ctx, c)
}

// Ask is open to any task participant except the owner, who is the addressee.
func (m *QuestionModule) Ask(ctx context.Context, token, key string, in api.Ask) (api.Snapshot, error) {
	return run(ctx, m.s, token, key, "commit.append", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if !validText(in.Body) {
			return nil, fault.ErrInput
		}
		t, pos, e := m.activeRound(tx, p, in.TaskID)
		if e != nil {
			return nil, e
		}
		if pos.RoundID != in.RoundID {
			return nil, fault.ErrConflict
		}
		if p.ID == t.Owner {
			return nil, fault.ErrInput
		}
		c, e := m.appendCommit(tx, p, t, "question", in.Body, "")
		if e != nil {
			return nil, e
		}
		if e = m.q.CreateQuestion(tx, question.Question{Tenant: t.Tenant, TaskID: t.ID, ID: id(), RoundID: pos.RoundID, CommitID: c.ID, Asker: p.ID, Addressee: t.Owner, Status: question.StatusOpen, CreatedAt: time.Now()}); e != nil {
			return nil, e
		}
		if e = m.p.RecordProgress(tx, p, t.Tenant, t.ID); e != nil {
			return nil, e
		}
		return m.s.finish(tx, t)
	})
}

// Answer is reserved to the human addressee and only for the current round.
// The flow is refreshed afterwards, so a completion held by the question can proceed.
func (m *QuestionModule) Answer(ctx context.Context, token, key string, in api.Answer) (api.Snapshot, error) {
	return run(ctx, m.s, token, key, "commit.append", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if !validText(in.Body) {
			return nil, fault.ErrInput
		}
		t, pos, e := m.activeRound(tx, p, in.TaskID)
		if e != nil {
			return nil, e
		}
		q, e := m.q.GetQuestion(tx, t.Tenant, t.ID, in.QuestionID)
		if e != nil {
			return nil, e
		}
		if p.Kind != "human" || p.ID != q.Addressee {
			return nil, az.ErrForbidden
		}
		if q.Status != question.StatusOpen || q.RoundID != pos.RoundID {
			return nil, fault.ErrConflict
		}
		c, e := m.appendCommit(tx, p, t, "answer", in.Body, q.CommitID)
		if e != nil {
			return nil, e
		}
		q.Status, q.AnswerCommitID, q.AnsweredBy = question.StatusAnswered, c.ID, p.ID
		if e = m.q.AnswerQuestion(tx, q); e != nil {
			return nil, e
		}
		return m.s.finish(tx, t)
	})
}
