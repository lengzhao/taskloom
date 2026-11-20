// Package question records requests for input raised inside a task round.
// The question and answer text live only in immutable commits; a Question
// keeps the references commits cannot carry. Who asked and when are the
// question commit's author and time; it is open until AnswerCommitID is set,
// and who answered is that commit's author.
package question

import "context"

// Question is open until its addressee answers it. It never becomes current
// again in another round: a question from an older round or an inactive task
// is obsolete without any write, so Restart/Cancel need not touch it.
type Question struct {
	Tenant, TaskID, ID, RoundID string
	CommitID, Addressee         string
	AnswerCommitID              string
}

// Reader is the read-only view used by queries and the completion gate.
type Reader interface {
	// OpenQuestions lists unanswered questions of exactly this round in the
	// order their commits were appended.
	OpenQuestions(ctx context.Context, tenant, taskID, roundID string) ([]Question, error)
	GetQuestion(ctx context.Context, tenant, taskID, id string) (Question, error)
}

// Store is used only by the question commands.
type Store interface {
	Reader
	// CreateQuestion requires the question commit to exist already.
	CreateQuestion(ctx context.Context, value Question) error
	// AnswerQuestion records AnswerCommitID; it fails with fault.ErrConflict
	// unless the question is still open.
	AnswerQuestion(ctx context.Context, value Question) error
}
