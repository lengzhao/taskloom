// Package catalog describes tenant workspace metadata and lightweight projects.
package catalog

import (
	"context"
	az "github.com/lengzhao/taskloom/cap/authorization"
)

type Space struct {
	Tenant, Name, Description string
	Version                   int64
}
type Project struct {
	Tenant, ID, Name, Description, Owner string
	Archived                             bool
	Version                              int64
}
type SpaceEdit struct {
	Name, Description string
	ExpectedVersion   int64
}
type ProjectEdit struct {
	ID, Name, Description, Owner string
	Archived                     bool
	ExpectedVersion              int64
}
type ProjectReader interface {
	GetProject(context.Context, string, string) (Project, error)
}
type Reader interface {
	ProjectReader
	GetSpace(context.Context, string) (Space, error)
	ListProjects(context.Context, string) ([]Project, error)
}
type Store interface {
	Reader
	SaveSpace(context.Context, Space, int64) error
	SaveProject(context.Context, Project, int64) error
}
type Service interface {
	People(context.Context, string) ([]az.MemberOption, error)
	Space(context.Context, string) (Space, error)
	EditSpace(context.Context, string, string, SpaceEdit) (Space, error)
	Projects(context.Context, string) ([]Project, error)
	SaveProject(context.Context, string, string, ProjectEdit) (Project, error)
}
