// Package plugins imports implemented providers for registration only.
// Unimplemented business kinds are deliberately not registered.
package plugins

import (
	_ "github.com/lengzhao/taskloom/plugins/api/health"
	_ "github.com/lengzhao/taskloom/plugins/server"
	_ "github.com/lengzhao/taskloom/plugins/transport/http"
)
