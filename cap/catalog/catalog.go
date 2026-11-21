// Package catalog describes tenant workspace metadata and lightweight projects.
package catalog

import "context"

type Space struct {
	Tenant, Name, Description string
	Version                   int64
}

// Project organizes tasks and grants access: its owner and Members read and
// take part in every task filed under it, as task participants do.
type Project struct {
	Tenant, ID, Name, Description, Owner string
	Members                              []string `json:",omitempty"`
	Archived                             bool
	Version                              int64
}

// Includes says whether subject is the project's owner or a member.
func (p Project) Includes(subject string) bool {
	if subject == "" {
		return false
	}
	if p.Owner == subject {
		return true
	}
	for _, m := range p.Members {
		if m == subject {
			return true
		}
	}
	return false
}

// MaxMembers bounds a project's member list.
const MaxMembers = 200

type SpaceEdit struct {
	Name, Description string
	ExpectedVersion   int64
}

// ProjectEdit replaces the whole project, Members included.
type ProjectEdit struct {
	ID, Name, Description, Owner string
	Members                      []string
	Archived                     bool
	ExpectedVersion              int64
}
type ProjectReader interface {
	GetProject(context.Context, string, string) (Project, error)
}
type Store interface {
	ProjectReader
	GetSpace(context.Context, string) (Space, error)
	ListProjects(context.Context, string) ([]Project, error)
	// SaveSpace and SaveProject create or replace; the catalog command checks
	// the client's expected version under the global write lock first.
	SaveSpace(context.Context, Space) error
	SaveProject(context.Context, Project) error
}

// Commands return only what they fixed and their receipts store only that:
// a space is identified by the tenant, a project by its ID. Callers read
// current facts through Space and Project, so a replay never shows stale ones.
type Service interface {
	Space(context.Context, string) (Space, error)
	EditSpace(context.Context, string, string, SpaceEdit) error
	Projects(context.Context, string) ([]Project, error)
	Project(ctx context.Context, credential, id string) (Project, error)
	SaveProject(context.Context, string, string, ProjectEdit) (projectID string, err error)
}
