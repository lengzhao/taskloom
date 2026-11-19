// Package question records requests for input raised inside a task round.
// The question and answer text live only in immutable commits; a Question
// keeps the references and the open/answered state that commits cannot carry.
package question

import (
	"context"
	"time"
)

const (
	StatusOpen     = "open"
	StatusAnswered = "answered"
)

// Question is open until its addressee answers it. It never becomes current
// again in another round: a question from an older round or an inactive task
// is obsolete without any write, so Return/Reopen/Cancel need not touch it.
type Question struct {
	Tenant, TaskID, ID, RoundID        string
	CommitID, Asker, Addressee         string
	Status, AnswerCommitID, AnsweredBy string
	CreatedAt                          time.Time
}

// Reader is the read-only view used by queries and the completion gate.
type Reader interface {
	// OpenQuestions lists unanswered questions of exactly this round, oldest first.
	OpenQuestions(ctx context.Context, tenant, taskID, roundID string) ([]Question, error)
	GetQuestion(ctx context.Context, tenant, taskID, id string) (Question, error)
}

// Store is used only by the question commands.
type Store interface {
	Reader
	CreateQuestion(ctx context.Context, value Question) error
	// AnswerQuestion fails with fault.ErrConflict unless the question is still open.
	AnswerQuestion(ctx context.Context, value Question) error
}
