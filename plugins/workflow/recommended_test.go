package workflow_test

import (
	"errors"
	"fmt"
	"testing"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/fault"
)

func names(specs []catalog.TagSpec) []string {
	out := []string{}
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// C26: recommended tags are only a source; an administrator imports the
// missing ones, skipping names the space already has, archived included.
func TestRecommendedTags(t *testing.T) {
	f := setup(t, nil)
	lead := f.human(t, "lead")
	c := f.catalog(t)
	missing, e := c.RecommendedTags(f.ctx, f.agent)
	if e != nil || len(missing) != 7 {
		t.Fatal("base recommends seven tags, readable by an Agent", names(missing), e)
	}
	if tags, _ := c.Tags(f.ctx, f.owner); len(tags) != 0 {
		t.Fatal("recommended tags were written without an import", len(tags))
	}
	// The space already has 安全 (archived) and 文档 under another case.
	sec := f.tag(t, "安全")
	if _, e = c.SaveTag(f.ctx, f.owner, "archive", catalog.TagEdit{ID: sec.ID, Name: sec.Name, Archived: true, ExpectedVersion: sec.Version}); e != nil {
		t.Fatal(e)
	}
	f.tag(t, "Docs")
	if _, e = c.SaveTag(f.ctx, f.owner, "doc", catalog.TagEdit{Name: "文档"}); e != nil {
		t.Fatal(e)
	}
	if missing, _ = c.RecommendedTags(f.ctx, f.owner); len(missing) != 5 {
		t.Fatal("existing and archived names still offered", names(missing))
	}
	for _, who := range []string{lead, f.agent} {
		if _, e = c.ImportRecommendedTags(f.ctx, who, "import-"+who[:4]); !errors.Is(e, az.ErrForbidden) {
			t.Fatal("a non-administrator imported", e)
		}
	}
	ids, e := c.ImportRecommendedTags(f.ctx, f.owner, "import")
	if e != nil || len(ids) != 5 {
		t.Fatal("import", ids, e)
	}
	tags, _ := c.Tags(f.ctx, f.owner)
	archived := 0
	for _, tag := range tags {
		if tag.Archived {
			archived++
		}
	}
	if len(tags) != 8 || archived != 1 {
		t.Fatal("archived recommendation came back or tags missing", len(tags), archived)
	}
	if ids, e = c.ImportRecommendedTags(f.ctx, f.owner, "again"); e != nil || len(ids) != 0 {
		t.Fatal("a second import added tags", ids, e)
	}
	if missing, _ = c.RecommendedTags(f.ctx, f.owner); len(missing) != 0 {
		t.Fatal("nothing left to recommend", names(missing))
	}
}

// The deployment replaces the list; an empty list recommends nothing and a
// broken list does not build.
func TestRecommendedTagsConfig(t *testing.T) {
	f := setup(t, map[string]any{"catalog": map[string]any{"config": map[string]any{"recommendedTags": []any{}}}})
	if missing, e := f.catalog(t).RecommendedTags(f.ctx, f.owner); e != nil || len(missing) != 0 {
		t.Fatal("empty list still recommends", names(missing), e)
	}
	for name, list := range map[string][]any{
		"duplicate": {map[string]any{"name": "a"}, map[string]any{"name": "A"}},
		"empty":     {map[string]any{"name": " "}},
	} {
		if _, e := setupErr(t, map[string]any{"catalog": map[string]any{"config": map[string]any{"recommendedTags": list}}}); e == nil {
			t.Fatal(name, "recommendation list built")
		}
	}
}

// Importing past the space's limit refuses the whole import.
func TestRecommendedTagsLimit(t *testing.T) {
	f := setup(t, nil)
	c := f.catalog(t)
	for i := 0; i < catalog.MaxTags-1; i++ {
		if _, e := c.SaveTag(f.ctx, f.owner, fmt.Sprintf("fill-%d", i), catalog.TagEdit{Name: fmt.Sprintf("t%d", i)}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := c.ImportRecommendedTags(f.ctx, f.owner, "over"); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("import past the limit", e)
	}
	if tags, _ := c.Tags(f.ctx, f.owner); len(tags) != catalog.MaxTags-1 {
		t.Fatal("a refused import left tags", len(tags))
	}
}
