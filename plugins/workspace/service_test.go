package workspace_test

import (
	"encoding/json"
	"github.com/lengzhao/taskloom/cap/plan"

	"context"
	"errors"
	"fmt"
	"github.com/lengzhao/taskloom/cap/record"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"path/filepath"
	"sync"
	"testing"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	api "github.com/lengzhao/taskloom/cap/workspace"
	access "github.com/lengzhao/taskloom/plugins/access/default"
	flow "github.com/lengzhao/taskloom/plugins/flow"
	local "github.com/lengzhao/taskloom/plugins/identity/local"
	"github.com/lengzhao/taskloom/plugins/security"
	store "github.com/lengzhao/taskloom/plugins/storage/sql"
	workspace "github.com/lengzhao/taskloom/plugins/workspace"
	"github.com/lengzhao/taskloom/runtime/command"
)

// noProgress stands in for the execution module; these tests cover human comments.
type noProgress struct{}

func (noProgress) RecordProgress(context.Context, identity.Principal, string, string) error {
	return nil
}

// noPlacer stands in for the plan module; these tests create no subtasks.
type noPlacer struct{}

func (noPlacer) Apply(context.Context, identity.Principal, plan.Placement) error {
	return fault.ErrInput
}

// readBack answers Create and Edit with current facts, as the HTTP API does:
// commands return only a Ref, the same Ref their receipt stores.
type readBack struct{ *workspace.Service }

func (r readBack) Create(ctx context.Context, token, key string, in api.Create) (api.Detail, error) {
	ref, e := r.Service.Create(ctx, token, key, in)
	return r.read(ctx, token, ref, e)
}
func (r readBack) Edit(ctx context.Context, token, key string, in api.Edit) (api.Detail, error) {
	ref, e := r.Service.Edit(ctx, token, key, in)
	return r.read(ctx, token, ref, e)
}
func (r readBack) read(ctx context.Context, token string, ref api.Ref, e error) (api.Detail, error) {
	if e != nil {
		return api.Detail{}, e
	}
	return r.Get(ctx, token, ref.TaskID, 0)
}

func fixture(t *testing.T) (readBack, *security.Service, string) {
	svc, sec, token, _ := fixtureWithStore(t)
	return svc, sec, token
}
func fixtureWithStore(t *testing.T) (readBack, *security.Service, string, *store.Store) {
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
	r, _ := command.New(command.Deps{Transactions: s, Identity: id, Authorization: az, Requests: s, Audit: s})
	sec := security.New(security.Deps{Passwords: s, Commands: r, Transactions: s, Principals: s, Credentials: s, Bootstrap: s, Members: s, Audit: s, Events: s})
	if e := sec.Bootstrap(context.Background(), security.AdminInput{Tenant: "t", Subject: "u", Operator: "test", Reason: "test", Password: "AdminPassword123!"}); e != nil {
		t.Fatal(e)
	}
	issued, e := sec.Login(context.Background(), capsecurity.LoginInput{Tenant: "t", Username: "u", Password: "AdminPassword123!"})
	if e != nil {
		t.Fatal(e)
	}
	f, e := flow.New(flow.Config{}, flow.Deps{Positions: s, Results: s, Reviews: s, Questions: s, Commits: s, Plan: s})
	if e != nil {
		t.Fatal(e)
	}
	inner, e := workspace.New(workspace.Deps{Catalog: s, Transactions: s, Tasks: s, Commits: s, Flow: f, Identity: id, Authorization: az, Commands: r, Events: s, Members: s, Participants: s, Progress: noProgress{}, Placer: noPlacer{}})
	if e != nil {
		t.Fatal(e)
	}
	return readBack{inner}, sec, issued.Secret, s
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
	if d.Position.RoundID == "" || d.Position.State != "ready" || d.Position.Waiting != "请补充任务目标" {
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
	_, e = s.Comment(ctx, token, "correction", api.Comment{TaskID: d.Task.ID, Body: "更正", RefID: c.CommitID})
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Get(ctx, token, d.Task.ID, 0)
	if e != nil || len(got.Commits) != 3 || got.Task.BasisVersion != 1 {
		t.Fatalf("get %+v %v", got, e)
	}
	edited, e := s.Edit(ctx, token, "edit", api.Edit{TaskID: d.Task.ID, Title: "目标", Goal: "交付", Criteria: "通过", BasisVersion: 1})
	if e != nil || edited.Task.BasisVersion != 2 || edited.Position.RoundID != d.Position.RoundID {
		t.Fatal("edit", e)
	}
	if _, e = s.Edit(ctx, token, "stale", api.Edit{TaskID: d.Task.ID, Title: "stale", BasisVersion: 1}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("stale", e)
	}
	page, e := s.Changes(ctx, token, start.Cursor)
	if e != nil || len(page.Changes) < 4 {
		t.Fatalf("changes %+v %v", page, e)
	}
	if e = sec.CreateUser(ctx, token, "second-admin", capsecurity.UserInput{Tenant: "t", Username: "admin2", Password: "UserPassword123!"}); e != nil {
		t.Fatal(e)
	}
	if e = sec.SetMember(ctx, token, "promote", az.Member{Tenant: "t", Subject: "admin2", Role: "admin", Active: true}); e != nil {
		t.Fatal(e)
	}
	second, e := sec.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: "admin2", Password: "UserPassword123!"})
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
func TestConcurrentCreateAndSharedCursorIsReauthorized(t *testing.T) {
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
	if e := sec.CreateUser(ctx, token, "user", capsecurity.UserInput{Tenant: "t", Username: "v", Password: "UserPassword123!"}); e != nil {
		t.Fatal(e)
	}
	issued, e := sec.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: "v", Password: "UserPassword123!"})
	if e != nil {
		t.Fatal(e)
	}
	page, e := s.Changes(ctx, token, "")
	if e != nil {
		t.Fatal(e)
	}
	// A cursor is only a position: another subject reusing it sees only what it may read.
	if _, e = s.Comment(ctx, token, "after-cursor", api.Comment{TaskID: first, Body: "private"}); e != nil {
		t.Fatal(e)
	}
	other, e := s.Changes(ctx, issued.Secret, page.Cursor)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range other.Changes {
		if c.TaskID == first {
			t.Fatal("cursor leaked an unreadable task")
		}
	}
	if _, e = s.Changes(ctx, issued.Secret, "not-a-cursor"); !errors.Is(e, fault.ErrInput) {
		t.Fatal("invalid cursor", e)
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
	if _, e = s.Comment(ctx, token, "bad-ref", api.Comment{TaskID: a.Task.ID, Body: "wrong", RefID: foreign.CommitID}); !errors.Is(e, fault.ErrNotFound) {
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
		if e := sec.CreateUser(ctx, admin, "create-"+name, capsecurity.UserInput{Tenant: "t", Username: name, Password: "UserPassword123!"}); e != nil {
			t.Fatal(e)
		}
		issued, e := sec.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: name, Password: "UserPassword123!"})
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
	if _, e = s.Edit(ctx, participant, "edit", api.Edit{TaskID: d.Task.ID, Title: "steal", BasisVersion: 1}); !errors.Is(e, az.ErrForbidden) {
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

func TestPeopleListsActiveMembersToActiveHumans(t *testing.T) {
	s, sec, admin, db := fixtureWithStore(t)
	ctx := context.Background()
	for _, name := range []string{"owner", "guest"} {
		if e := sec.CreateUser(ctx, admin, "create-"+name, capsecurity.UserInput{Tenant: "t", Username: name, Password: "UserPassword123!"}); e != nil {
			t.Fatal(e)
		}
	}
	if e := sec.Create(ctx, admin, "create-agent", identity.Principal{Tenant: "t", ID: "agent", Kind: "agent", OwnerID: "owner"}); e != nil {
		t.Fatal(e)
	}
	issue := func(id string) string {
		t.Helper()
		v, e := sec.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: id, Password: "UserPassword123!"})
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
	// Any active human member reads the directory; it is not task-scoped.
	for _, token := range []string{admin, owner, guest} {
		list, e := s.People(ctx, token)
		if e != nil || len(list) != 4 {
			t.Fatalf("people: %+v %v", list, e)
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
	if e = sec.SetMember(ctx, admin, "disable-agent", az.Member{Tenant: "t", Subject: "agent", Role: "member", Active: false}); e != nil {
		t.Fatal(e)
	}
	list, e := s.People(ctx, owner)
	if e != nil || len(list) != 3 {
		t.Fatalf("inactive candidate: %+v %v", list, e)
	}
	if e = db.Do(ctx, func(tx context.Context) error {
		return db.CreatePrincipal(tx, identity.Principal{Tenant: "other", ID: "foreign", Kind: "human"})
	}); e != nil {
		t.Fatal(e)
	}
	if e := sec.Recover(ctx, security.AdminInput{Tenant: "other", Subject: "foreign", Operator: "test", Reason: "isolation test", Password: "AdminPassword123!"}); e != nil {
		t.Fatal(e)
	}
	foreign, e := sec.Login(ctx, capsecurity.LoginInput{Tenant: "other", Username: "foreign", Password: "AdminPassword123!"})
	if e != nil {
		t.Fatal(e)
	}
	// A foreign tenant's member reads only its own tenant's directory.
	if other, e := s.People(ctx, foreign.Secret); e != nil || len(other) != 1 || other[0].Subject != "foreign" {
		t.Fatal("cross-tenant directory", other, e)
	}
	list, e = s.People(ctx, owner)
	if e != nil || len(list) != 3 {
		t.Fatalf("cross-tenant member leaked: %+v %v", list, e)
	}
	if e = sec.SetMember(ctx, admin, "disable-owner", az.Member{Tenant: "t", Subject: "owner", Role: "member", Active: false}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.People(ctx, owner); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("revoked owner", e)
	}
}

// Receipts keep only the Ref: a replayed create or comment returns the same
// Ref, and the task read afterwards shows facts written since, not the old ones.
func TestReplayReturnsSameRefAndReceiptStoresNoFacts(t *testing.T) {
	s, _, token, db := fixtureWithStore(t)
	ctx := context.Background()
	in := api.Create{Title: "旧标题", Goal: "goal", Criteria: "criteria"}
	first, e := s.Service.Create(ctx, token, "create", in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Service.Edit(ctx, token, "edit", api.Edit{TaskID: first.TaskID, Title: "新标题", Goal: "goal", Criteria: "criteria", BasisVersion: 1}); e != nil {
		t.Fatal(e)
	}
	replayed, e := s.Service.Create(ctx, token, "create", in)
	if e != nil || replayed != first {
		t.Fatal("replay changed the ref", replayed, first, e)
	}
	if d, e := s.Get(ctx, token, replayed.TaskID, 0); e != nil || d.Task.Title != "新标题" {
		t.Fatal("current facts not visible after replay", d.Task.Title, e)
	}
	comment, e := s.Service.Comment(ctx, token, "comment", api.Comment{TaskID: first.TaskID, Body: "留言"})
	if e != nil || comment.CommitID == "" || comment.TaskID != first.TaskID {
		t.Fatal("comment ref", comment, e)
	}
	for op, key := range map[string]string{"task.create": "create", "task.edit": "edit", "commit.append": "comment"} {
		stored, e := db.FindReceipt(ctx, record.RequestKey{Tenant: "t", Subject: "u", Operation: op, Key: key})
		if e != nil {
			t.Fatal(op, e)
		}
		var fields map[string]any
		if e = json.Unmarshal(stored.Result, &fields); e != nil {
			t.Fatal(op, e)
		}
		for k := range fields {
			if k != "TaskID" && k != "CommitID" {
				t.Fatal("receipt stores derivable facts", op, k)
			}
		}
	}
}
