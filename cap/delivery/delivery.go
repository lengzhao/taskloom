// Package delivery registers a round's result. A Result only links the
// execution to the immutable result commit, which identifies it; author, time
// and basis version are that commit's facts and are read from it.
package delivery

import "context"

type Result struct {
	Tenant, TaskID, RoundID, ExecutionID, CommitID string
}
type Reader interface {
	CurrentResult(ctx context.Context, tenant, taskID, roundID string) (Result, error)
}
type Store interface {
	Reader
	RegisterResult(ctx context.Context, value Result) error
}
