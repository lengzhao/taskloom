package security

import (
	"github.com/lengzhao/pluginkit"
	domain "github.com/lengzhao/taskloom/domain/security"
	"github.com/lengzhao/taskloom/runtime/command"
)

func init() {
	pluginkit.Register("commands/runtime", func(_ struct{}, d command.Deps) (*command.Runner, error) { return command.New(d) })
	pluginkit.Register("commands/security", func(_ struct{}, d domain.Deps) (*domain.Service, error) { return domain.New(d), nil })
}
