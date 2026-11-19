package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Keep GORM and SQL drivers out of public contracts, domain and runtime code.
func TestDatabaseAndPluginImplementationBoundaries(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if (strings.HasPrefix(imported, "gorm.io/") || imported == "database/sql" || strings.Contains(imported, "go-sql-driver") || strings.Contains(imported, "go-sqlite3") || strings.Contains(imported, "pgx")) && !strings.HasPrefix(relative, "plugins/storage/sql/") {
				t.Errorf("database implementation leaked: %s -> %s", relative, imported)
			}
			// Domain packages hold plain rules: no command lifecycle, wiring or plugins.
			if strings.HasPrefix(relative, "domain/") && (strings.HasPrefix(imported, "github.com/lengzhao/taskloom/runtime/") || strings.HasPrefix(imported, "github.com/lengzhao/taskloom/plugins/") || strings.HasPrefix(imported, "github.com/lengzhao/pluginkit")) {
				t.Errorf("domain package depends on runtime or plugins: %s -> %s", relative, imported)
			}
			prefix := "github.com/lengzhao/taskloom/plugins/"
			if strings.HasPrefix(imported, prefix) && !strings.HasPrefix(relative, "cmd/") && relative != "plugins/all.go" {
				t.Errorf("concrete plugin import: %s -> %s", relative, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Typed Deps are plugin wiring; domain packages must stay dependency-free rules.
func TestDomainDeclaresNoDeps(t *testing.T) {
	root := filepath.Join("..", "..", "domain")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			if g, ok := decl.(*ast.GenDecl); ok {
				for _, spec := range g.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok && strings.HasSuffix(ts.Name.Name, "Deps") {
						t.Errorf("domain declares plugin dependencies: %s %s", path, ts.Name.Name)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
