package security

import (
	"context"

	"github.com/lengzhao/taskloom/cap/authorization"
)

type MemberCommands interface {
	SetMember(ctx context.Context, credential, idempotencyKey string, value authorization.Member) error
}
