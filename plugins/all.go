// Package plugins imports implemented providers for registration only.
// Unimplemented business kinds are deliberately not registered.
package plugins

import (
	_ "github.com/lengzhao/taskloom/plugins/access/default"
	_ "github.com/lengzhao/taskloom/plugins/api/checkpoint"
	_ "github.com/lengzhao/taskloom/plugins/api/health"
	_ "github.com/lengzhao/taskloom/plugins/api/workflow"
	_ "github.com/lengzhao/taskloom/plugins/api/workspace"
	_ "github.com/lengzhao/taskloom/plugins/catalog"
	_ "github.com/lengzhao/taskloom/plugins/flow"
	_ "github.com/lengzhao/taskloom/plugins/identity/local"
	_ "github.com/lengzhao/taskloom/plugins/security"
	_ "github.com/lengzhao/taskloom/plugins/server"
	_ "github.com/lengzhao/taskloom/plugins/state/accepting"
	_ "github.com/lengzhao/taskloom/plugins/state/checkpoint"
	_ "github.com/lengzhao/taskloom/plugins/state/completed"
	_ "github.com/lengzhao/taskloom/plugins/state/executing"
	_ "github.com/lengzhao/taskloom/plugins/state/factcheck"
	_ "github.com/lengzhao/taskloom/plugins/state/ready"
	_ "github.com/lengzhao/taskloom/plugins/storage/sql"
	_ "github.com/lengzhao/taskloom/plugins/transport/http"
	_ "github.com/lengzhao/taskloom/plugins/workflow"
	_ "github.com/lengzhao/taskloom/plugins/workspace"
)
