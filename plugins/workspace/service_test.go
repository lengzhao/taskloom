package workspace_test

import (
	"context"
	"errors"
	"fmt"
	capflow "github.com/lengzhao/taskloom/cap/flow"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"path/filepath"
	"sync"
	"testing"
	"time"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	api "github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/domain/security"
	access "github.com/lengzhao/taskloom/plugins/access/default"
	flow "github.com/lengzhao/taskloom/plugins/flow"
	local "github.com/lengzhao/taskloom/plugins/identity/local"
	ready "github.com/lengzhao/taskloom/plugins/state/ready"
	store "github.com/lengzhao/taskloom/plugins/storage/sql"
	workspace "github.com/lengzhao/taskloom/plugins/workspace"
	"github.com/lengzhao/taskloom/runtime/command"
)

func fixture(t *testing.T) (*workspace.Service, *security.Service, string) {
	svc, sec, token, _ := fixtureWithStore(t)
	return svc, sec, token
}
func fixtureWithStore(t *testing.T) (*workspace.Service, *security.Service, string, *store.Store) {
	t.Helper()
	s, e := store.New(store.Config{DSN: filepath.Join(t.TempDir(), "db")})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Open(context.Background()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	id, _ := local.New(local.Deps{Store: s})
	az, _ := access.New(access.Deps{Store: s, Tasks: s})
	r, _ := command.New(command.Deps{Transactions: s, Identity: id, Authorization: az, Requests: s, Audit: s, Events: s})
	sec := security.New(security.Deps{Passwords: s, Commands: r, Transactions: s, Principals: s, Credentials: s, Bootstrap: s, Members: s, Audit: s, Events: s})
	issued, e := sec.Bootstrap(context.Background(), security.BootstrapInput{Tenant: "t", Subject: "u", Operator: "test", Reason: "test", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	f, e := flow.New(flow.Config{ID: "preparation", Version: "1"}, flow.Deps{Definitions: s, Positions: s, States: []capflow.State{&ready.State{}}})
	if e != nil {
		t.Fatal(e)
	}
	svc, e := workspace.New(workspace.Deps{Catalog: s, Transactions: s, Tasks: s, Commits: s, Flow: f, Identity: id, Authorization: az, Commands: r, Events: s, Members: s, Participants: s})
	if e != nil {
		t.Fatal(e)
	}
	return svc, sec, issued.Secret, s
}

func TestCreateReadCommitEditAndRevoke(t *testing.T) {
	s, sec, token := fixture(t)
	ctx := context.Background()

	start, e := s.Changes(ctx, token, "")
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.Create(ctx, token, "create", api.Create{Title: "目标"})
	if e != nil {
		t.Fatal(e)
	}
	if d.Position.RoundID == "" || d.Position.EntryID == "" || d.Position.State != "ready" || d.Position.Waiting != "请补充任务目标" {
		t.Fatalf("position %+v", d.Position)
	}
	again, e := s.Create(ctx, token, "create", api.Create{Title: "目标"})
	if e != nil || again.Task.ID != d.Task.ID {
		t.Fatal("idempotency", e)
	}
	c, e := s.Comment(ctx, token, "comment", api.Comment{TaskID: d.Task.ID, Body: "初始"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Comment(ctx, token, "correction", api.Comment{TaskID: d.Task.ID, Body: "更正", RefID: c.ID})
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Get(ctx, token, d.Task.ID, 0)
	if e != nil || len(got.Commits) != 3 || got.Task.Version != 1 {
		t.Fatalf("get %+v %v", got, e)
	}
	edited, e := s.Edit(ctx, token, "edit", api.Edit{TaskID: d.Task.ID, Title: "目标", Goal: "交付", Criteria: "通过", ExpectedVersion: 1})
	if e != nil || edited.Task.BasisVersion != 2 || edited.Position.RoundID != d.Position.RoundID {
		t.Fatal("edit", e)
	}
	if _, e = s.Edit(ctx, token, "stale", api.Edit{TaskID: d.Task.ID, Title: "stale", ExpectedVersion: 1}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("stale", e)
	}
	page, e := s.Changes(ctx, token, start.Cursor)
	if e != nil || len(page.Changes) < 4 {
		t.Fatalf("changes %+v %v", page, e)
	}
	if e = sec.Create(ctx, token, "second-admin", identity.Principal{Tenant: "t", ID: "admin2", Kind: "human"}); e != nil {
		t.Fatal(e)
	}
	if e = sec.SetMember(ctx, token, "promote", az.Member{Tenant: "t", Subject: "admin2", Role: "admin", Active: true}); e != nil {
		t.Fatal(e)
	}
	second, e := sec.Issue(ctx, token, "admin2-token", capsecurity.IssueInput{Tenant: "t", Subject: "admin2", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	if e = sec.SetMember(ctx, second.Secret, "deactivate", az.Member{Tenant: "t", Subject: "u", Role: "member", Active: false}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Comment(ctx, token, "comment", api.Comment{TaskID: d.Task.ID, Body: "初始"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("revoked replay", e)
	}
	list, e := s.List(ctx, token)
	if !errors.Is(e, az.ErrForbidden) || len(list) != 0 {
		t.Fatal("revoked list", e)
	}
	page, e = s.Changes(ctx, token, start.Cursor)
	if !errors.Is(e, az.ErrForbidden) {
		t.Fatal("revoked changes", page, e)
	}
}
func TestConcurrentCreateAndCrossSubjectCursor(t *testing.T) {
	s, sec, token := fixture(t)
	ctx := context.Background()

	ids := make(chan string, 6)
	errs := make(chan error, 6)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, e := s.Create(ctx, token, "same", api.Create{Title: "one"})
			ids <- d.Task.ID
			errs <- e
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("duplicate tasks")
		}
	}
	if e := sec.Create(ctx, token, "user", identity.Principal{Tenant: "t", ID: "v", Kind: "human"}); e != nil {
		t.Fatal(e)
	}
	issued, e := sec.Issue(ctx, token, "issue", capsecurity.IssueInput{Tenant: "t", Subject: "v", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	page, e := s.Changes(ctx, token, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Changes(ctx, issued.Secret, page.Cursor); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("cursor subject", e)
	}
	if _, e = s.Get(ctx, issued.Secret, first, 0); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("read", e)
	}
}

func TestCommitPaginationAndForeignReference(t *testing.T) {
	s, _, token := fixture(t)
	ctx := context.Background()

	a, e := s.Create(ctx, token, "a", api.Create{Title: "a"})
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Create(ctx, token, "b", api.Create{Title: "b"})
	if e != nil {
		t.Fatal(e)
	}
	foreign, e := s.Comment(ctx, token, "foreign", api.Comment{TaskID: b.Task.ID, Body: "other task"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Comment(ctx, token, "bad-ref", api.Comment{TaskID: a.Task.ID, Body: "wrong", RefID: foreign.ID}); !errors.Is(e, fault.ErrNotFound) {
		t.Fatal("foreign ref", e)
	}
	for i := 0; i < 103; i++ {
		if _, e = s.Comment(ctx, token, fmt.Sprintf("c%d", i), api.Comment{TaskID: a.Task.ID, Body: "page"}); e != nil {
			t.Fatal(e)
		}
	}
	first, e := s.Get(ctx, token, a.Task.ID, 0)
	if e != nil || len(first.Commits) != 100 {
		t.Fatal("first page", len(first.Commits), e)
	}
	next, e := s.Get(ctx, token, a.Task.ID, first.Commits[99].Sequence)
	if e != nil || len(next.Commits) != 4 {
		t.Fatal("next page", len(next.Commits), e)
	}
}

func TestMembershipOwnerParticipantsAndImmediateRemoval(t *testing.T) {
	s, sec, admin := fixture(t)
	ctx := context.Background()
	createUser := func(name string) string {
		t.Helper()
		if e := sec.Create(ctx, admin, "create-"+name, identity.Principal{Tenant: "t", ID: name, Kind: "human"}); e != nil {
			t.Fatal(e)
		}
		issued, e := sec.Issue(ctx, admin, "issue-"+name, capsecurity.IssueInput{Tenant: "t", Subject: name, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		return issued.Secret
	}
	owner := createUser("owner")
	participant := createUser("participant")
	outsider := createUser("outsider")
	d, e := s.Create(ctx, owner, "create", api.Create{Title: "owner task"})
	if e != nil {
		t.Fatal(e)
	}
	if d.Task.Owner != "owner" {
		t.Fatal("owner not bound")
	}
	if _, e = s.Get(ctx, outsider, d.Task.ID, 0); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("unrelated member read", e)
	}
	in := api.Participation{TaskID: d.Task.ID, Subject: "participant", Present: true}
	if e = s.SetParticipant(ctx, owner, "join", in); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, participant, d.Task.ID, 0); e != nil {
		t.Fatal(e)
	}
	comment := api.Comment{TaskID: d.Task.ID, Body: "participant comment"}
	if _, e = s.Comment(ctx, participant, "comment", comment); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Edit(ctx, participant, "edit", api.Edit{TaskID: d.Task.ID, Title: "steal", ExpectedVersion: 1}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("participant edited", e)
	}
	if e = s.SetParticipant(ctx, participant, "self-add", api.Participation{TaskID: d.Task.ID, Subject: "outsider", Present: true}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("participant delegated", e)
	}
	start, e := s.Changes(ctx, participant, "")
	if e != nil {
		t.Fatal(e)
	}
	in.Present = false
	if e = s.SetParticipant(ctx, owner, "remove", in); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Comment(ctx, participant, "comment", comment); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("removed replay", e)
	}
	page, e := s.Changes(ctx, participant, start.Cursor)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range page.Changes {
		if c.TaskID != "" {
			t.Fatal("removed change leaked")
		}
	}
	in.Present = true
	if e = s.SetParticipant(ctx, owner, "rejoin", in); e != nil {
		t.Fatal(e)
	}
	if e = sec.SetMember(ctx, admin, "disable", az.Member{Tenant: "t", Subject: "participant", Role: "member", Active: false}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, participant, d.Task.ID, 0); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("inactive member read", e)
	}
	if _, e = s.Comment(ctx, participant, "comment", comment); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("inactive replay", e)
	}
	if e = sec.SetMember(ctx, owner, "escalate", az.Member{Tenant: "t", Subject: "owner", Role: "admin", Active: true}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("member self-promotion", e)
	}
	if e = sec.SetMember(ctx, admin, "cross-tenant", az.Member{Tenant: "other", Subject: "owner", Role: "admin", Active: true}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("cross tenant member", e)
	}
	if _, e = s.Get(ctx, admin, d.Task.ID, 0); e != nil {
		t.Fatal("admin", e)
	}
}

func TestCandidatesRequireTaskManagementAndFilterInactiveMembers(t *testing.T) {
	s, sec, admin, db := fixtureWithStore(t)
	ctx := context.Background()
	for _, p := range []identity.Principal{{Tenant: "t", ID: "owner", Kind: "human"}, {Tenant: "t", ID: "guest", Kind: "human"}, {Tenant: "t", ID: "agent", Kind: "agent", OwnerID: "owner"}} {
		if e := sec.Create(ctx, admin, "create-"+p.ID, p); e != nil {
			t.Fatal(e)
		}
	}
	issue := func(id string) string {
		t.Helper()
		v, e := sec.Issue(ctx, admin, "issue-"+id, capsecurity.IssueInput{Tenant: "t", Subject: id, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		return v.Secret
	}
	owner, guest := issue("owner"), issue("guest")
	d, e := s.Create(ctx, owner, "task", api.Create{Title: "directory"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetParticipant(ctx, owner, "join", api.Participation{TaskID: d.Task.ID, Subject: "guest", Present: true}); e != nil {
		t.Fatal(e)
	}
	for _, token := range []string{admin, owner} {
		list, e := s.Candidates(ctx, token, d.Task.ID)
		if e != nil || len(list) != 4 {
			t.Fatalf("candidates: %+v %v", list, e)
		}
		found := false
		for _, m := range list {
			if m.Subject == "agent" && m.Kind == "agent" && m.OwnerID == "owner" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing agent metadata")
		}
	}
	if _, e = s.Candidates(ctx, guest, d.Task.ID); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("participant sees directory", e)
	}
	if _, e = s.Candidates(ctx, owner, "missing"); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("missing task", e)
	}
	if e = sec.SetMember(ctx, admin, "disable-agent", az.Member{Tenant: "t", Subject: "agent", Role: "member", Active: false}); e != nil {
		t.Fatal(e)
	}
	list, e := s.Candidates(ctx, owner, d.Task.ID)
	if e != nil || len(list) != 3 {
		t.Fatalf("inactive candidate: %+v %v", list, e)
	}
	if e = db.Do(ctx, func(tx context.Context) error {
		return db.CreatePrincipal(tx, identity.Principal{Tenant: "other", ID: "foreign", Kind: "human"})
	}); e != nil {
		t.Fatal(e)
	}
	foreign, e := sec.Recover(ctx, security.BootstrapInput{Tenant: "other", Subject: "foreign", Operator: "test", Reason: "isolation test", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Candidates(ctx, foreign.Secret, d.Task.ID); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("cross-tenant access", e)
	}
	list, e = s.Candidates(ctx, owner, d.Task.ID)
	if e != nil || len(list) != 3 {
		t.Fatalf("cross-tenant member leaked: %+v %v", list, e)
	}
	if e = sec.SetMember(ctx, admin, "disable-owner", az.Member{Tenant: "t", Subject: "owner", Role: "member", Active: false}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Candidates(ctx, owner, d.Task.ID); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("revoked owner", e)
	}
}
