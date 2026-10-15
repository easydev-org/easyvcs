package store

import (
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// The object Bloom filter must never report a false NEGATIVE (a stored id must
// always be found); false positives are acceptable and bounded.
func TestObjectBloomNoFalseNegative(t *testing.T) {
	bl := NewObjectBloom(1000)
	ids := make([]object.ID, 0, 1000)
	for i := 0; i < 1000; i++ {
		id := object.BlobID([]byte{byte(i), byte(i >> 8), 'x'})
		ids = append(ids, id)
		bl.Add(id)
	}
	for _, id := range ids {
		if !bl.MayContain(id) {
			t.Fatalf("stored id %s reported absent (false negative)", id)
		}
	}
}

// Round-trip through Encode/Decode preserves membership.
func TestObjectBloomRoundTrip(t *testing.T) {
	bl := NewObjectBloom(500)
	kept := make([]object.ID, 0, 500)
	for i := 0; i < 500; i++ {
		id := object.BlobID([]byte{byte(i), 'y'})
		bl.Add(id)
		kept = append(kept, id)
	}
	dec := DecodeObjectBloom(bl.Encode())
	if dec == nil {
		t.Fatal("decode returned nil")
	}
	for _, id := range kept {
		if !dec.MayContain(id) {
			t.Fatalf("decoded filter lost id %s", id)
		}
	}
	// An empty string decodes to nil (no filter).
	if DecodeObjectBloom("") != nil {
		t.Fatal("empty string should decode to nil")
	}
}

// False-positive rate is bounded (well under 5% for 1%-sized filters).
func TestObjectBloomFalsePositiveRate(t *testing.T) {
	const n = 2000
	bl := NewObjectBloom(n)
	for i := 0; i < n; i++ {
		bl.Add(object.BlobID([]byte{byte(i), byte(i >> 8), 'z'}))
	}
	fp := 0
	const probes = 20000
	for i := 0; i < probes; i++ {
		// Distinct content from the inserted ones.
		id := object.BlobID([]byte("absent-" + string(rune(i%127)) + string(rune((i/127)%127)) + string(rune(i/16000))))
		if bl.MayContain(id) {
			fp++
		}
	}
	if rate := float64(fp) / probes; rate > 0.05 {
		t.Fatalf("false-positive rate %.3f too high", rate)
	}
}

// ObjectBloom lists the store's objects: a stored object is always present, an
// unstored one is (almost always) absent.
func TestStoreObjectBloom(t *testing.T) {
	cs := newTestCentral(t)
	repo, _ := cs.Create(NSRef{Name: "bloomrepo"})
	blob := &object.Object{Kind: object.KindBlob, Blob: []byte("hello bloom")}
	if err := repo.WriteObject(blob); err != nil {
		t.Fatal(err)
	}
	bl, err := cs.ObjectBloom()
	if err != nil {
		t.Fatal(err)
	}
	if !bl.MayContain(blob.ID()) {
		t.Fatal("stored object absent from the store bloom")
	}
	if bl.MayContain(object.BlobID([]byte("definitely-not-stored-xyz"))) {
		t.Fatal("absent object reported present")
	}
}
