package server

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/rr"
	"github.com/easylab-platform/easyvcs/store"
)

// seedConflict builds a namespace with a landed conflict at path, returning the
// server mux, token and repo. It uses the store + rr service directly (as the
// other RR tests do).
func seedConflict(t *testing.T, path string, base, ours, theirs []byte, isBinary bool) (*http.ServeMux, string) {
	t.Helper()
	s := newTestServer(t, "secret")
	u, _ := s.cs.GetUserByUsername("tester")
	repo, _ := s.cs.Create(store.NSRef{Owner: u.ID, Name: "team-app"})
	makePublic(t, repo)
	ws := revision.NewWorkspace(repo)
	write := func(content []byte) object.ID {
		tr := object.NewTree()
		if isBinary {
			blob, _ := ws.WriteBlob(content)
			tr.Entries[path] = object.Entry{Name: path, Kind: object.KindBlob, ID: blob}
		} else {
			blob, _ := ws.WriteBlob(content)
			tr.Entries[path] = object.Entry{Name: path, Kind: object.KindBlob, ID: blob}
		}
		id, _ := ws.WriteTree(tr)
		return id
	}
	baseRev, _, _ := ws.Commit(revision.CommitParams{TreeID: write(base), Description: "base", Author: store.Author{Name: "t"}})
	_, _ = ws.SetRef("main", store.RefBranch, baseRev.RevisionID)
	src, _, _ := ws.Commit(revision.CommitParams{Parents: []object.ID{baseRev.RevisionHash}, TreeID: write(ours), Description: "ours", Author: store.Author{Name: "t"}})
	adv, _, _ := ws.Commit(revision.CommitParams{Parents: []object.ID{baseRev.RevisionHash}, TreeID: write(theirs), Description: "theirs", Author: store.Author{Name: "t"}})
	_, _ = ws.SetRef("main", store.RefBranch, adv.RevisionID)

	svc := rr.New(s.cs, repo)
	req, err := svc.Create(rr.Source{Snapshot: src.RevisionHash}, "main", "conflict", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyMergeOpts(req, store.Author{Name: "t"}, nil, rr.MergeOptions{AllowConflict: true}); err != nil {
		t.Fatal(err)
	}
	return s.Router(), "secret"
}

func TestConflictViewText(t *testing.T) {
	mux, token := seedConflict(t, "f.txt", []byte("a\nb\nc\n"), []byte("a\nOURS\nc\n"), []byte("a\nTHEIRS\nc\n"), false)
	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/conflict?ref=main&path=f.txt", token, nil)
	if rec.Code != 200 {
		t.Fatalf("conflict view: %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	terms, _ := body["terms"].([]any)
	if len(terms) != 3 {
		t.Fatalf("expected 3 terms, got %d: %v", len(terms), terms)
	}
	byRole := map[string]map[string]any{}
	for _, tv := range terms {
		m := tv.(map[string]any)
		byRole[m["role"].(string)] = m
	}
	if byRole["ours"]["content"] != "a\nOURS\nc\n" {
		t.Fatalf("ours content: %v", byRole["ours"])
	}
	if byRole["theirs"]["content"] != "a\nTHEIRS\nc\n" {
		t.Fatalf("theirs content: %v", byRole["theirs"])
	}
	if byRole["base"]["content"] != "a\nb\nc\n" {
		t.Fatalf("base content: %v", byRole["base"])
	}
}

func TestConflictViewBinaryAndObject(t *testing.T) {
	bin := func(s byte) []byte { return []byte{0x89, 'P', 'N', 'G', 0, s} }
	mux, token := seedConflict(t, "l.png", bin(1), bin(2), bin(3), true)
	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/conflict?ref=main&path=l.png", token, nil)
	if rec.Code != 200 {
		t.Fatalf("conflict view: %d %s", rec.Code, rec.Body.String())
	}
	terms := decode(t, rec)["terms"].([]any)
	var binSha string
	for _, tv := range terms {
		m := tv.(map[string]any)
		if m["kind"] == "blob" {
			if m["is_binary"] != true {
				t.Fatalf("expected is_binary: %v", m)
			}
			if _, has := m["content"]; has {
				t.Fatalf("binary term must not inline content: %v", m)
			}
			binSha = m["sha"].(string)
		}
	}
	if binSha == "" {
		t.Fatal("no binary sha")
	}
	raw := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/object?sha="+binSha, token, nil)
	if raw.Code != 200 {
		t.Fatalf("object: %d %s", raw.Code, raw.Body.String())
	}
	got := raw.Body.Bytes()
	if len(got) != 6 || got[0] != 0x89 || got[1] != 'P' {
		t.Fatalf("object bytes wrong: %v", got)
	}
	_ = base64.StdEncoding
	_ = httptest.NewRequest
}
