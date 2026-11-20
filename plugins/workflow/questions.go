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

// QuestionDeps has nine fields: the command lifecycle, the task and its flow
// position, the timeline that holds the text, the question records, the
// executor's progress, the relations that validate an addressee, the
// participant write that lets a newly asked member read the task, and the
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
	// Participants adds an addressee who is not yet on the task, so they can read and answer.
	Participants az.ParticipantWriter `json:"participants"`
	Events       record.EventWriter   `json:"events"`
}
type QuestionModule struct{ d QuestionDeps }

func init() { pluginkit.Register("commands/question", NewQuestions) }

func NewQuestions(_ struct{}, d QuestionDeps) (*QuestionModule, error) {
	if d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Commits == nil || d.Questions == nil || d.Progress == nil || d.Relations == nil || d.Participants == nil || d.Events == nil {
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

// addressee resolves who a question is asked of: the owner by default, or any
// active member of the tenant. A member not yet on the task is added as a
// participant in the same transaction, since answering needs reading the task.
// Nobody asks themselves.
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
	if e != nil || !member.Active || (member.Role != "admin" && member.Role != "member") {
		return "", fault.ErrInput
	}
	who := az.Participant{Tenant: t.Tenant, TaskID: t.ID, Subject: to}
	in, e := m.d.Relations.HasParticipant(ctx, who)
	if e != nil || in {
		return to, e
	}
	if e = m.d.Participants.SetParticipant(ctx, who, true); e != nil {
		return "", e
	}
	// Participation changes who may read the task, so every viewer refreshes access.
	return to, m.d.Events.AppendEvent(ctx, record.Event{Tenant: t.Tenant, Resource: t.ID, Kind: record.EventAccessChanged})
}

// Ask is open to any task participant; it waits on the addressee, who may be
// any active member.
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

// Close settles an open question of the current round without an answer.
// The asker may withdraw it and the owner may release the task from it; the
// addressee answers instead. The reason is the closing commit.
func (m *QuestionModule) Close(ctx context.Context, token, key string, in api.CloseQuestion) (api.Ref, error) {
	return run(ctx, m.d.Commands, token, key, "commit.append", in.TaskID, in, func(tx context.Context, p identity.Principal) (api.Ref, error) {
		if !validText(in.Reason) {
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
		asked, e := m.d.Commits.Get(tx, t.Tenant, t.ID, q.CommitID)
		if e != nil {
			return api.Ref{}, e
		}
		if p.ID != asked.Author && p.ID != t.Owner {
			return api.Ref{}, az.ErrForbidden
		}
		if q.AnswerCommitID != "" || q.RoundID != pos.RoundID {
			return api.Ref{}, fault.ErrConflict
		}
		c, e := m.appendCommit(tx, p, t, api.KindQuestionClosed, in.Reason, q.CommitID)
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
