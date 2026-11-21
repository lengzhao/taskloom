package file_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/fault"
	capfile "github.com/lengzhao/taskloom/cap/file"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/plugins/worker/blobsweep"
)

// pending claims every pending blob key; in these tests nothing is in flight
// when it is called, so a non-empty result is a leaked record.
func (f *fixture) pending() []string {
	f.t.Helper()
	tx, _ := build.GetByID[unitofwork.Manager](f.graph, "sql")
	store, _ := build.GetByID[capfile.PendingBlobs](f.graph, "sql")
	var keys []string
	err := tx.Do(context.Background(), func(ctx context.Context) error {
		var e error
		keys, e = store.ClaimBlobs(ctx, time.Now().Add(time.Hour), 1000)
		return e
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return keys
}

func (f *fixture) sweeper() *blobsweep.Sweeper {
	s, ok := build.GetByID[*blobsweep.Sweeper](f.graph, "blob-sweep")
	if !ok {
		f.t.Fatal("sweeper not built")
	}
	return s
}

// eventually waits for the server to finish work the client no longer sees.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type brokenBody struct{ sent bool }

func (b *brokenBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, "partial bytes"), nil
	}
	return 0, errors.New("client went away")
}

// An upload whose client disconnects midway leaves no file, bytes or record.
func TestInterruptedUploadLeavesNothing(t *testing.T) {
	f := setup(t)
	id := f.task("i")
	req, _ := http.NewRequest("POST", f.srv.URL+"/v1/tasks/"+id+"/files?name=x.txt", &brokenBody{})
	req.Header.Set("Authorization", "Bearer "+f.owner)
	req.Header.Set("Idempotency-Key", "broken")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("interrupted upload accepted")
		}
	}
	eventually(t, "interrupted upload left bytes", func() bool { return f.objects() == 0 })
	var listed []file
	if f.json("GET", "/tasks/"+id+"/files", f.owner, "", nil, &listed); len(listed) != 0 {
		t.Fatal("interrupted upload listed", listed)
	}
	if keys := f.pending(); len(keys) != 0 {
		t.Fatal("pending records left", keys)
	}
	// The same key then uploads normally: the failure stored no receipt.
	if code, _ := f.upload(id, f.owner, "broken", "x.txt", []byte("whole")); code != 200 {
		t.Fatal("retry", code)
	}
}

// Deleting is the uploader's own withdrawal of an unreferenced upload.
func TestDeleteOwnUnreferencedUpload(t *testing.T) {
	f := setup(t)
	id := f.task("d")
	if code := f.json("POST", "/tasks/"+id+"/participants", f.owner, "p", map[string]any{"Subject": "agent", "Present": true}, nil); code != 200 {
		t.Fatal("participant", code)
	}
	_, mine := f.upload(id, f.agent, "m", "mine.txt", []byte("mine"))
	_, kept := f.upload(id, f.agent, "k", "kept.txt", []byte("kept"))
	if code := f.json("POST", "/tasks/"+id+"/commits", f.agent, "c", map[string]any{"Files": []string{kept.ID}}, nil); code != 200 {
		t.Fatal("reference", code)
	}
	del := func(token, key, fid string) int {
		code, _ := f.do("DELETE", "/tasks/"+id+"/files/"+fid, token, key, "", nil)
		return code
	}
	// Another participant cannot see, so cannot delete, an unreferenced upload.
	if code := del(f.owner, "d0", mine.ID); code != 404 {
		t.Fatal("deleted another's upload", code)
	}
	// A referenced file stays with its commit, for everyone.
	for _, tok := range []string{f.agent, f.owner} {
		if code := del(tok, "d1", kept.ID); code != 409 {
			t.Fatal("deleted referenced file", code)
		}
	}
	if code := del(f.other, "d2", mine.ID); code != 403 {
		t.Fatal("outsider delete", code)
	}
	if code := del(f.agent, "", mine.ID); code != 400 && code != 409 {
		t.Fatal("delete without idempotency key", code)
	}
	if code := del(f.agent, "d3", mine.ID); code != 200 {
		t.Fatal("delete own upload", code)
	}
	// A replay succeeds again; a new request for the gone file is not found.
	if code := del(f.agent, "d3", mine.ID); code != 200 {
		t.Fatal("delete replay", code)
	}
	if code := del(f.agent, "d4", mine.ID); code != 404 {
		t.Fatal("delete twice", code)
	}
	if code, _ := f.do("GET", "/tasks/"+id+"/files/"+mine.ID, f.agent, "", "", nil); code != 404 {
		t.Fatal("deleted file readable", code)
	}
	if code := f.json("POST", "/tasks/"+id+"/commits", f.agent, "c2", map[string]any{"Files": []string{mine.ID}}, nil); code != 404 {
		t.Fatal("deleted file referenced", code)
	}
	if n := f.objects(); n != 1 {
		t.Fatal("stored objects after delete", n)
	}
	if keys := f.pending(); len(keys) != 0 {
		t.Fatal("pending records left", keys)
	}
}

// A reference and a delete of the same upload race: exactly one wins, and the
// result is consistent — a referenced file keeps its bytes, a deleted one has
// neither row nor bytes.
func TestReferenceAndDeleteRace(t *testing.T) {
	f := setup(t)
	id := f.task("race")
	const rounds = 20
	referenced := 0
	for i := 0; i < rounds; i++ {
		_, up := f.upload(id, f.owner, fmt.Sprint("u", i), "r.txt", []byte(fmt.Sprint("round ", i)))
		var wg sync.WaitGroup
		var refCode, delCode int
		wg.Add(2)
		go func() {
			defer wg.Done()
			raw := fmt.Sprintf(`{"Files":[%q]}`, up.ID)
			refCode, _ = f.do("POST", "/tasks/"+id+"/commits", f.owner, fmt.Sprint("c", i), "application/json", []byte(raw))
		}()
		go func() {
			defer wg.Done()
			delCode, _ = f.do("DELETE", "/tasks/"+id+"/files/"+up.ID, f.owner, fmt.Sprint("d", i), "", nil)
		}()
		wg.Wait()
		switch {
		case refCode == 200 && delCode == 409:
			referenced++
			code, body := f.do("GET", "/tasks/"+id+"/files/"+up.ID, f.owner, "", "", nil)
			if code != 200 || string(body) != fmt.Sprint("round ", i) {
				t.Fatal("referenced file lost its bytes", code)
			}
		case refCode == 404 && delCode == 200:
			if code, _ := f.do("GET", "/tasks/"+id+"/files/"+up.ID, f.owner, "", "", nil); code != 404 {
				t.Fatal("deleted file still readable", code)
			}
		default:
			t.Fatalf("round %d: reference %d, delete %d", i, refCode, delCode)
		}
	}
	if n := f.objects(); n != referenced {
		t.Fatal("objects", n, "referenced", referenced)
	}
	if keys := f.pending(); len(keys) != 0 {
		t.Fatal("pending records left", keys)
	}
}

// Historical files follow current access: removing a participant or
// deactivating a member stops reads of files it could read before.
func TestRevokedAccessToHistoricalFiles(t *testing.T) {
	f := setup(t)
	id := f.task("h")
	if code := f.json("POST", "/tasks/"+id+"/participants", f.owner, "p1", map[string]any{"Subject": "agent", "Present": true}, nil); code != 200 {
		t.Fatal("participant", code)
	}
	_, up := f.upload(id, f.owner, "u", "h.txt", []byte("history"))
	f.json("POST", "/tasks/"+id+"/commits", f.owner, "c", map[string]any{"Files": []string{up.ID}}, nil)
	if code, _ := f.do("GET", "/tasks/"+id+"/files/"+up.ID, f.agent, "", "", nil); code != 200 {
		t.Fatal("participant read", code)
	}
	f.json("POST", "/tasks/"+id+"/participants", f.owner, "p2", map[string]any{"Subject": "agent", "Present": false}, nil)
	for _, path := range []string{"/files/" + up.ID, "/files"} {
		if code, _ := f.do("GET", "/tasks/"+id+path, f.agent, "", "", nil); code != 403 {
			t.Fatal("removed participant read", path, code)
		}
	}
	f.json("POST", "/tasks/"+id+"/participants", f.owner, "p3", map[string]any{"Subject": "agent", "Present": true}, nil)
	if code := f.json("DELETE", "/members", f.owner, "m", map[string]any{"Tenant": "t", "Subject": "agent", "Role": "member"}, nil); code != 200 {
		t.Fatal("deactivate", code)
	}
	if code, _ := f.do("GET", "/tasks/"+id+"/files/"+up.ID, f.agent, "", "", nil); code != 403 && code != 401 {
		t.Fatal("deactivated member read", code)
	}
}

// Crash between storing bytes and registering them leaves a reservation; the
// sweeper deletes those bytes. A reservation claimed while its upload is still
// writing cannot be registered, so a slow upload fails instead of exposing a
// file whose bytes were swept.
func TestSweeperCleansAndFencesUploads(t *testing.T) {
	f := setup(t)
	id := f.task("s")
	ctx := context.Background()
	tx, _ := build.GetByID[unitofwork.Manager](f.graph, "sql")
	store, _ := build.GetByID[capfile.Store](f.graph, "sql")
	blobs, _ := build.GetByID[interface {
		Put(context.Context, string, io.Reader) error
	}](f.graph, "blobs")
	crashed := "00112233445566778899aabbccddeeff"
	if err := tx.Do(ctx, func(c context.Context) error { return store.ReserveBlob(c, crashed) }); err != nil {
		t.Fatal(err)
	}
	if err := blobs.Put(ctx, crashed, strings.NewReader("orphan")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if n, err := f.sweeper().Sweep(ctx); err != nil || n != 1 {
		t.Fatal("sweep", n, err)
	}
	if f.objects() != 0 || len(f.pending()) != 0 {
		t.Fatal("orphan left")
	}
	// A claimed reservation is not registrable.
	claimed := "ffeeddccbbaa99887766554433221100"
	if err := tx.Do(ctx, func(c context.Context) error { return store.ReserveBlob(c, claimed) }); err != nil {
		t.Fatal(err)
	}
	if keys := f.pending(); len(keys) != 1 {
		t.Fatal("claim", keys)
	}
	err := tx.Do(ctx, func(c context.Context) error {
		return store.RegisterFile(c, capfile.File{Tenant: "t", TaskID: id, ID: claimed, Name: "x", Uploader: "owner"})
	})
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatal("registered a claimed reservation", err)
	}
	time.Sleep(5 * time.Millisecond) // past the 1ms grace
	if n, err := f.sweeper().Sweep(ctx); err != nil || n != 1 {
		t.Fatal("sweep claimed", n, err)
	}

	// End to end: the sweeper claims a slow upload's reservation mid-transfer.
	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("POST", f.srv.URL+"/v1/tasks/"+id+"/files?name=slow.txt", pr)
		req.Header.Set("Authorization", "Bearer "+f.owner)
		req.Header.Set("Idempotency-Key", "slow")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	_, _ = pw.Write([]byte("first half "))
	eventually(t, "reservation not swept", func() bool {
		n, _ := f.sweeper().Sweep(ctx)
		return n > 0
	})
	_, _ = pw.Write([]byte("second half"))
	_ = pw.Close()
	if code := <-done; code != 409 {
		t.Fatal("upload after its reservation was swept", code)
	}
	var listed []file
	if f.json("GET", "/tasks/"+id+"/files", f.owner, "", nil, &listed); len(listed) != 0 {
		t.Fatal("swept upload listed", listed)
	}
	if f.objects() != 0 || len(f.pending()) != 0 {
		t.Fatal("swept upload left bytes or records")
	}
}
