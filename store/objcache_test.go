package store

import (
	"fmt"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// A repeated ReadObject of the same id must hit the cache (one DB read).
func TestObjectCacheHit(t *testing.T) {
	cs := newTestCentral(t)
	repo, _ := cs.Create(NSRef{Name: "cache"})
	blob := &object.Object{Kind: object.KindBlob, Blob: []byte("cache me")}
	if err := repo.WriteObject(blob); err != nil {
		t.Fatal(err)
	}
	// Read several times: the first may hit (WriteObject seeds the cache) or
	// miss; subsequent reads must all hit.
	for i := 0; i < 5; i++ {
		o, err := repo.ReadObject(blob.ID())
		if err != nil {
			t.Fatal(err)
		}
		if string(o.Blob) != "cache me" {
			t.Fatalf("wrong content: %q", o.Blob)
		}
	}
	hits, misses := cs.ObjectCacheStats()
	if hits < 4 {
		t.Fatalf("expected >=4 cache hits, got %d (misses %d)", hits, misses)
	}
}

// The cache is bounded: inserting more than capacity evicts the LRU.
func TestObjectCacheEvicts(t *testing.T) {
	cs := newTestCentral(t)
	cs.WithObjectCache(4)
	repo, _ := cs.Create(NSRef{Name: "evict"})
	ids := make([]object.ID, 0, 8)
	for i := 0; i < 8; i++ {
		o := &object.Object{Kind: object.KindBlob, Blob: []byte(fmt.Sprintf("b%d", i))}
		_ = repo.WriteObject(o)
		ids = append(ids, o.ID())
	}
	// The cache holds at most 4 entries.
	cs.objCache.mu.Lock()
	n := len(cs.objCache.items)
	cs.objCache.mu.Unlock()
	if n > 4 {
		t.Fatalf("cache holds %d entries, want <=4", n)
	}
	// A read still returns the right content even after eviction (falls back to DB).
	for _, id := range ids {
		if _, err := repo.ReadObject(id); err != nil {
			t.Fatalf("read after eviction: %v", err)
		}
	}
}

// A disabled cache (capacity 0) never panics and never reports hits.
func TestObjectCacheDisabled(t *testing.T) {
	c := newObjectCache(0)
	if c != nil {
		t.Fatal("capacity 0 must yield a nil cache")
	}
	if _, ok := c.get(object.BlobID([]byte("x"))); ok {
		t.Fatal("nil cache must not hit")
	}
	c.put(object.BlobID([]byte("x")), &object.Object{}) // must not panic
}
