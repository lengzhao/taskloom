package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/skills"
)

func TestSkillRegistryInvariants(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "skills.db"))
	ctx := context.Background()
	write := func(f func(tx context.Context) error) error { return s.Do(ctx, f) }
	c := skills.Capability{Tenant: "t", ID: "c1", Name: "notes", Owner: "u", Visibility: skills.Private}
	if err := write(func(tx context.Context) error { return s.CreateCapability(tx, c) }); err != nil {
		t.Fatal(err)
	}
	// The name is unique within the tenant, not across tenants.
	if err := write(func(tx context.Context) error {
		return s.CreateCapability(tx, skills.Capability{Tenant: "t", ID: "c2", Name: "notes"})
	}); !errors.Is(err, fault.ErrConflict) {
		t.Fatal("duplicate name", err)
	}
	if err := write(func(tx context.Context) error {
		return s.CreateCapability(tx, skills.Capability{Tenant: "t2", ID: "c1", Name: "notes"})
	}); err != nil {
		t.Fatal("other tenant", err)
	}
	if err := write(func(tx context.Context) error {
		return s.SaveCapability(tx, skills.Capability{Tenant: "t", ID: "missing", Name: "x"})
	}); !errors.Is(err, fault.ErrNotFound) {
		t.Fatal("save of a missing capability", err)
	}
	// A capability without a release is never listed.
	if list, _ := s.ListCapabilities(ctx, "t"); len(list) != 0 {
		t.Fatal("listed without release", list)
	}

	appendRelease := func(ref, version string) error {
		return write(func(tx context.Context) error {
			return s.AppendSkillRelease(tx, skills.Release{Tenant: "t", CapabilityID: "c1", Ref: ref, Name: "notes", Version: version})
		})
	}
	if err := appendRelease("r1", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := appendRelease("r2", "1.0.0"); !errors.Is(err, fault.ErrConflict) {
		t.Fatal("second release of one version", err)
	}
	if err := write(func(tx context.Context) error { return s.SetSkillVector(tx, "t", "r1", []float32{0.5, -1}) }); err != nil {
		t.Fatal(err)
	}
	// The current release is the last one appended; its predecessor's vector goes.
	if err := appendRelease("r3", "1.1.0"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.CurrentSkillRelease(ctx, "t", "c1"); cur.Ref != "r3" {
		t.Fatal("current", cur)
	}
	if list, _ := s.ListCapabilities(ctx, "t"); len(list) != 1 || list[0].Release.Ref != "r3" || list[0].Owner != "u" {
		t.Fatal("list", list)
	}
	if vs, _ := s.SkillVectors(ctx, "t", []string{"r1", "r3"}); len(vs) != 0 {
		t.Fatal("superseded vector kept", vs)
	}
	if err := write(func(tx context.Context) error { return s.SetSkillVector(tx, "t", "r1", []float32{1}) }); !errors.Is(err, fault.ErrNotFound) {
		t.Fatal("vector for a superseded release", err)
	}
	if missing, _ := s.MissingSkillVectors(ctx, "t"); len(missing) != 1 || missing[0].Ref != "r3" {
		t.Fatal("missing vectors", missing)
	}
	if err := write(func(tx context.Context) error { return s.SetSkillVector(tx, "t", "r3", []float32{0.5, -1}) }); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.SkillVectors(ctx, "t", []string{"r3"}); vs["r3"][1] != -1 {
		t.Fatal("vector round trip", vs)
	}

	for _, v := range []string{skills.Approved, skills.Rejected} {
		if err := write(func(tx context.Context) error {
			return s.AppendSkillReview(tx, skills.Review{Tenant: "t", Ref: "r3", Verdict: v, Reviewer: "admin"})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if latest, _ := s.LatestSkillReview(ctx, "t", "r3"); latest.Verdict != skills.Rejected {
		t.Fatal("latest review", latest)
	}
	if _, err := s.LatestSkillReview(ctx, "t", "r1"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatal("no review", err)
	}
	c.Archived = true
	if err := write(func(tx context.Context) error { return s.SaveCapability(tx, c) }); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListCapabilities(ctx, "t"); len(list) != 0 {
		t.Fatal("archived listed", list)
	}
}
