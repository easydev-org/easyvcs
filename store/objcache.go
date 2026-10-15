package store

import (
	"container/list"
	"sync"

	"github.com/easylab-platform/easyvcs/object"
)

// lru is a bounded, LRU cache of any value keyed by string. Snapshots and
// revisions are immutable once written (a snapshot hash covers its content; a
// revision's row only changes hash on rewrite, which re-puts the cache), so a
// cached value never goes stale — eviction is purely about memory.
type lru[V any] struct {
	mu       sync.Mutex
	ll       *list.List
	items    map[string]*list.Element
	capacity int
}

type lruEntry[V any] struct {
	key string
	val V
}

func newLRU[V any](capacity int) *lru[V] {
	if capacity <= 0 {
		return nil
	}
	return &lru[V]{ll: list.New(), items: make(map[string]*list.Element, capacity), capacity: capacity}
}

func (c *lru[V]) get(key string) (V, bool) {
	var zero V
	if c == nil {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*lruEntry[V]).val, true
	}
	return zero, false
}

func (c *lru[V]) put(key string, v V) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*lruEntry[V]).val = v
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&lruEntry[V]{key: key, val: v})
	c.items[key] = el
	if c.ll.Len() > c.capacity {
		back := c.ll.Back()
		if back != nil {
			c.ll.Remove(back)
			delete(c.items, back.Value.(*lruEntry[V]).key)
		}
	}
}

// objectCache is a bounded, LRU cache of decoded objects keyed by id. Objects
// are content-addressed and IMMUTABLE, so a cached value never goes stale — the
// only reason to evict is memory. It absorbs the repeated tree/blob reads of a
// browse/log/git-build burst (the DB read is the cost).
//
// The cache is per CentralStore (process-local). It holds *object.Object values
// whose Blob/Tree slices must be treated as read-only by callers (they are the
// cached copies); the workspace never mutates a decoded object in place.
type objectCache struct {
	mu       sync.Mutex
	ll       *list.List               // front = most recently used
	items    map[string]*list.Element // id -> element (value *objectEntry)
	capacity int
	hits     uint64
	misses   uint64
}

type objectEntry struct {
	id  string
	obj *object.Object
}

func newObjectCache(capacity int) *objectCache {
	if capacity <= 0 {
		return nil // disabled
	}
	return &objectCache{ll: list.New(), items: make(map[string]*list.Element, capacity), capacity: capacity}
}

// get returns the cached object for id, moving it to the front.
func (c *objectCache) get(id object.ID) (*object.Object, bool) {
	if c == nil {
		return nil, false
	}
	key := id.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		c.hits++
		return el.Value.(*objectEntry).obj, true
	}
	c.misses++
	return nil, false
}

// put inserts (or refreshes) an object, evicting the LRU entry when full.
func (c *objectCache) put(id object.ID, o *object.Object) {
	if c == nil {
		return
	}
	key := id.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*objectEntry).obj = o
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&objectEntry{id: key, obj: o})
	c.items[key] = el
	if c.ll.Len() > c.capacity {
		back := c.ll.Back()
		if back != nil {
			c.ll.Remove(back)
			delete(c.items, back.Value.(*objectEntry).id)
		}
	}
}

// stats returns (hits, misses) for tests/observability.
func (c *objectCache) stats() (uint64, uint64) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}
