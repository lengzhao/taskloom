package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	_ "github.com/lengzhao/taskloom/plugins"
	cfg "github.com/lengzhao/taskloom/runtime/config"
)

type settings struct {
	Label  string   `json:"label"`
	Items  []string `json:"items"`
	Secret string   `json:"secret"`
}

func (c *settings) Validate() error {
	if c.Label == "" {
		return errors.New("label required")
	}
	if c.Label == "reject" {
		return errors.New("sensitive " + c.Secret)
	}
	return nil
}

type dependencies struct {
	Next lifecycle.Component `json:"next,omitempty"`
}
type node struct {
	Settings settings
	Next     lifecycle.Component
}

func (*node) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (*node) Stop(context.Context) error    { return nil }

type root struct {
	Components []lifecycle.Component `json:"components"`
}

func (*root) Run(context.Context) error  { return nil }
func (*root) Stop(context.Context) error { return nil }

type securityDeps struct {
	Identity      identity.Authenticator   `json:"identity"`
	Authorization authorization.Authorizer `json:"authorization"`
}

func init() {
	pluginkit.Register("test/security-probe", func(_ struct{}, d securityDeps) (*node, error) {
		if d.Identity == nil || d.Authorization == nil {
			return nil, errors.New("missing security deps")
		}
		return &node{}, nil
	})
	pluginkit.Register("test/root", func(_ struct{}, d root) (*root, error) { return &d, nil })
	pluginkit.Register("test/component", func(c settings, d dependencies) (*node, error) { return &node{c, d.Next}, nil })
	pluginkit.Register("test/other", func(c struct {
		Enabled bool `json:"enabled"`
	}) (*node, error) {
		return &node{Settings: settings{Label: "other"}}, nil
	})
	pluginkit.Register("test/wrong", func() (*struct{}, error) { return &struct{}{}, nil })
}

const base = `server:
  use: server
  deps:
    resources: []
    components: [first, second]
first:
  use: test/component
  config:
    label: base
    items: [one, two]
second:
  use: test/component
  config:
    label: second
`

func files(t *testing.T, b, o string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	bp := filepath.Join(dir, "base.yaml")
	op := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(bp, []byte(b), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(op, []byte(o), 0600); err != nil {
		t.Fatal(err)
	}
	return bp, op
}
func TestRealBuildMergesConfigAndReplacesLists(t *testing.T) {
	bp, op := files(t, base, "first:\n  config:\n    label: overridden\n    items: [three]\nserver:\n  deps:\n    components: [second, first]\n")
	_, result, err := cfg.BuildFiles(context.Background(), bp, op)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := build.GetByID[*node](result, "first")
	if !ok {
		t.Fatal("missing actual PluginKit instance")
	}
	if first.Settings.Label != "overridden" || strings.Join(first.Settings.Items, ",") != "three" {
		t.Fatalf("bad merge: %+v", first.Settings)
	}
	if len(result.Instances) != 3 {
		t.Fatalf("unexpected instances: %d", len(result.Instances))
	}
}
func TestChangedKindReplacesWholeNode(t *testing.T) {
	bp, op := files(t, base, "first:\n  use: test/other\n  config:\n    enabled: true\n")
	_, result, err := cfg.BuildFiles(context.Background(), bp, op)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := build.GetByID[*node](result, "first")
	if n.Settings.Label != "other" {
		t.Fatal("replacement not built")
	}
}
func TestDisabledUnusedNodeAndRequiredDependency(t *testing.T) {
	for _, tt := range []struct {
		name, overlay string
		wantErr       bool
	}{
		{"unused", "first: null\nserver:\n  deps:\n    components: [second]\n", false},
		{"required", "first: null\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bp, op := files(t, base, tt.overlay)
			_, _, err := cfg.BuildFiles(context.Background(), bp, op)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
func TestAssemblyFailures(t *testing.T) {
	for _, tt := range []struct{ name, overlay string }{
		{"unknown kind", "first:\n  use: missing/kind\n"},
		{"missing dependency", "server:\n  deps:\n    components: [missing]\n"},
		{"wrong dependency type", "first:\n  use: test/wrong\n"},
		{"cycle", "first:\n  deps:\n    next: second\nsecond:\n  deps:\n    next: first\n"},
		{"bad field type", "first:\n  config:\n    items: wrong\n"},
		{"unknown field", "first:\n  config:\n    typo: true\n"},
		{"invalid config", "first:\n  config:\n    label: ''\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bp, op := files(t, base, tt.overlay)
			_, _, err := cfg.BuildFiles(context.Background(), bp, op)
			if err == nil {
				t.Fatal("expected real PluginKit failure")
			}
		})
	}
}
func TestDiagnosticsNeverExposeConfigurationValues(t *testing.T) {
	for _, overlay := range []string{"first:\n  config:\n    label: reject\n    secret: DO_NOT_LOG_THIS_SECRET\n", "first: [DO_NOT_LOG_THIS_SECRET\n", "first:\n  use: DO_NOT_LOG_THIS_SECRET\n"} {
		bp, op := files(t, base, overlay)
		_, _, err := cfg.BuildFiles(context.Background(), bp, op)
		if err == nil {
			t.Fatal("expected failure")
		}
		if strings.Contains(err.Error(), "DO_NOT_LOG_THIS_SECRET") {
			t.Fatalf("leaked diagnostic: %v", err)
		}
	}
}
func TestMissingExplicitFileAndDuplicateYAMLFail(t *testing.T) {
	bp, op := files(t, base, "")
	if err := os.Remove(op); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cfg.BuildFiles(context.Background(), bp, op); err == nil {
		t.Fatal("missing overlay must fail")
	}
	bp, op = files(t, base, "first: {}\nfirst: {}\n")
	if _, _, err := cfg.BuildFiles(context.Background(), bp, op); err == nil {
		t.Fatal("duplicate YAML accepted")
	}
	bp, op = files(t, base, "{}\n---\nfirst: {}\n")
	if _, _, err := cfg.BuildFiles(context.Background(), bp, op); err == nil {
		t.Fatal("third document accepted")
	}
}
func TestUnreachablePluginIsNotConstructed(t *testing.T) {
	bp, op := files(t, base+"unused:\n  use: missing/kind\n", "")
	_, result, err := cfg.BuildFiles(context.Background(), bp, op)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instances) != 3 {
		t.Fatal("unreachable node was built")
	}
}
func TestInlineDependencyUsesPluginKit(t *testing.T) {
	bp, op := files(t, "server:\n  use: server\n  deps:\n    resources: []\n    components:\n      - use: test/component\n        config:\n          label: inline\n", "")
	_, result, err := cfg.BuildFiles(context.Background(), bp, op)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instances) != 2 {
		t.Fatal("inline not built")
	}
}

// Business definition validation belongs to the reachable owning plugin, not
// the generic YAML loader. A dormant legacy/future node must not be interpreted.
func TestLoaderDoesNotInterpretBusinessFlowConfig(t *testing.T) {
	bp, op := files(t, base+"unused:\n  use: task/catalog\n  config:\n    defaultFlow:\n      id: missing\n      version: '1'\n", "")
	if _, _, err := cfg.BuildFiles(context.Background(), bp, op); err != nil {
		t.Fatalf("loader interpreted unreachable business configuration: %v", err)
	}
}

func TestReachableUnimplementedFlowStillFails(t *testing.T) {
	bp, op := files(t, "server:\n  use: server\n  deps:\n    resources: [flow]\n    components: []\nflow:\n  use: task/flow\n  config:\n    id: example\n    version: '1'\n", "")
	if _, _, err := cfg.BuildFiles(context.Background(), bp, op); err == nil {
		t.Fatal("unimplemented reachable business plugin accepted")
	}
}

func TestCommentOnlyOverlayIsEmpty(t *testing.T) {
	for _, overlay := range []string{"# deployment defaults\n", " \n \t", "{}", ""} {
		bp, op := files(t, base, overlay)
		if _, _, err := cfg.BuildFiles(context.Background(), bp, op); err != nil {
			t.Fatalf("overlay %q: %v", overlay, err)
		}
	}
}

func TestDependencyListOrderIsInjected(t *testing.T) {
	bp, op := files(t, strings.Replace(base, "use: server", "use: test/root", 1), "server:\n  deps:\n    components: [second, first]\n")
	// The fixture root consumes only components, so omit resources from its base.
	raw, _ := os.ReadFile(bp)
	if err := os.WriteFile(bp, []byte(strings.Replace(string(raw), "    resources: []\n", "", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	app, _, err := cfg.BuildFiles(context.Background(), bp, op)
	if err != nil {
		t.Fatal(err)
	}
	r := app.(*root)
	if r.Components[0].(*node).Settings.Label != "second" || r.Components[1].(*node).Settings.Label != "base" {
		t.Fatal("dependency list order changed")
	}
}

// Root defaults now promise only implemented capabilities. Exercise the actual
// PluginKit builder, not just YAML shape, without opening resources/listeners.
func TestRootDefaultGraphBuilds(t *testing.T) {
	app, _, err := cfg.BuildFiles(context.Background(), "../../base.yaml", "../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if app == nil {
		t.Fatal("missing root application")
	}
}

func TestRetiredFlowOverlaysPreserveCurrentDefaults(t *testing.T) {
	expected, err := cfg.LoadFiles("../../base.yaml", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"standard", "no-review"} {
		t.Run(name, func(t *testing.T) {
			actual, err := cfg.LoadFiles("../../base.yaml", "../../examples/flows/"+name+".config.yaml")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatal("retired overlay changed runnable graph")
			}
		})
	}
}

// Build a typed consumer to prove identity/access use Deps, not a Config slot.
func TestSecurityPluginsComposeThroughDeps(t *testing.T) {
	bp, op := files(t, `server:
  use: server
  deps:
    resources: [sql]
    components: [probe]
sql:
  use: storage/sql
  config:
    dsn: ':memory:'
identity:
  use: identity/local
  deps:
    store: sql
access:
  use: access/default
  deps:
    store: sql
    tasks: sql
probe:
  use: test/security-probe
  deps:
    identity: identity
    authorization: access
`, "")
	if _, _, err := cfg.BuildFiles(context.Background(), bp, op); err != nil {
		t.Fatal(err)
	}
}
