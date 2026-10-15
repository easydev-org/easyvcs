package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
	"github.com/easylab-platform/easyvcs/transfer"
)

// Object-level delta: when advertise returns a Bloom filter of the server's
// objects, doPushHTTP skips objects the server already holds — so the pushed
// bundle does not re-send unchanged blobs/trees.
func TestPushUsesObjectBloomDelta(t *testing.T) {
	// SERVER store: holds rev1 only.
	serverHome := t.TempDir()
	t.Setenv("EASYVCS_HOME", serverHome)
	scs, _ := store.OpenDefault()
	srv, _ := scs.Create(store.NSRef{Name: "delta"})
	sws := revision.NewWorkspace(srv)
	_, sr1, err := sws.CommitFromChanges(object.ID{}, []revision.FileChangeSpec{
		{Path: "keep.txt", Content: []byte("keep-unchanged")},
		{Path: "a.txt", Content: []byte("v1")},
	}, "c1", store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = sws.SetRef("main", store.RefBranch, sr1.ID)
	// A WELL-SIZED bloom of the server's objects (a real server has thousands,
	// so the filter is large; a tiny store would give a high false-positive
	// rate and the test would be flaky). We list the server store's object ids
	// and insert them into a generously-sized filter.
	srvIDs, err := scs.AllObjectIDs()
	if err != nil {
		t.Fatal(err)
	}
	bloom := store.NewObjectBloom(len(srvIDs) + 1000)
	for _, id := range srvIDs {
		bloom.Add(id)
	}

	// CLIENT store: has rev1 (same content) + rev2 (a.txt -> v2).
	clientHome := t.TempDir()
	t.Setenv("EASYVCS_HOME", clientHome)
	ccs, _ := store.OpenDefault()
	src, _ := ccs.Create(store.NSRef{Name: "delta"})
	ws := revision.NewWorkspace(src)
	_, r1, err := ws.CommitFromChanges(object.ID{}, []revision.FileChangeSpec{
		{Path: "keep.txt", Content: []byte("keep-unchanged")},
		{Path: "a.txt", Content: []byte("v1")},
	}, "c1", store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := ws.GetSnapshot(r1.Hash)
	_, r2, err := ws.CommitFromChanges(s1.RevisionHash, []revision.FileChangeSpec{
		{Path: "a.txt", Content: []byte("v2")},
	}, "c2", store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ws.SetRef("main", store.RefBranch, r2.ID)
	_ = sr1

	// The client pushes rev2 only, WITH the bloom: the unchanged keep.txt blob
	// (which the server already has) must be SKIPPED.
	adv := advertiseResp{
		Repo:         "delta",
		Changes:      []string{r1.ID}, // server already has rev1
		ChangeHashes: map[string]string{r1.ID: r1.Hash.String()},
		ObjectBloom:  bloom.Encode(),
	}
	have := serverHaveSet(src, adv)
	dec := store.DecodeObjectBloom(adv.ObjectBloom)
	b, err := transfer.Collect(src, have, func(id object.ID) bool { return dec.MayContain(id) })
	if err != nil {
		t.Fatal(err)
	}

	// The keep.txt blob's CONTENT must NOT be in the bundle (server has it).
	hasKeep, hasV2 := false, false
	for _, o := range b.Objects {
		if o.Kind == object.KindBlob && string(o.Content) == "keep-unchanged" {
			hasKeep = true
		}
		if o.Kind == object.KindBlob && string(o.Content) == "v2" {
			hasV2 = true
		}
	}
	if hasKeep {
		t.Fatal("unchanged blob was re-sent despite the server Bloom containing it")
	}
	if !hasV2 {
		t.Fatal("changed blob a.txt v2 is missing from the bundle")
	}
}

// A server advertise that carries an object_bloom field is honored by the CLI
// decoder (round-trip through JSON).
func TestAdvertiseBloomJSONRoundTrip(t *testing.T) {
	bl := store.NewObjectBloom(10)
	id := object.BlobID([]byte("x"))
	bl.Add(id)
	raw, _ := json.Marshal(advertiseResp{ObjectBloom: bl.Encode()})
	var got advertiseResp
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	dec := store.DecodeObjectBloom(got.ObjectBloom)
	if dec == nil || !dec.MayContain(id) {
		t.Fatal("bloom lost through JSON round-trip")
	}
	_ = httptest.NewServer
	_ = http.MethodPost
}
