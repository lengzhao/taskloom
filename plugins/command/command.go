// Package command registers the shared command lifecycle (transaction,
// idempotency receipt and audit) implemented in runtime/command.
package command

import (
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/runtime/command"
)

func init() {
	pluginkit.Register("commands/runtime", func(_ struct{}, d command.Deps) (*command.Runner, error) { return command.New(d) })
}
