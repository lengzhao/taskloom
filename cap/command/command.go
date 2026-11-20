// Package command defines the shared atomic lifecycle of named use cases.
// It does not dispatch operations or locate domain services.
package command

import (
	"context"
	"github.com/lengzhao/taskloom/cap/identity"
)

type Request struct {
	Token, Operation, Key, Resource string
	// Authorize is a server-supplied named-use-case check, never parsed from HTTP.
	// It is run before both new writes and receipt replay. Nil uses fixed rules.
	Authorize func(context.Context, identity.Principal) error
}

// Handler checks current identity/relationships before replay, then atomically
// commits the explicit callback and required receipt/audit/events. Digest is
// computed from the typed domain input by runtime/command.Run.
type Handler interface {
	Do(ctx context.Context, request Request, digest string, apply func(context.Context, identity.Principal) ([]byte, error)) ([]byte, error)
}
