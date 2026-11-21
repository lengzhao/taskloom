package file_test

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

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

// An upload whose client disconnects midway leaves no file or bytes.
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
	// The same key then uploads normally: the failure stored no receipt.
	if code, _ := f.upload(id, f.owner, "broken", "x.txt", []byte("whole")); code != 200 {
		t.Fatal("retry", code)
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
