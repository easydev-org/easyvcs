package server

import (
	"sync"
	"time"

	"github.com/easylab-platform/easyvcs/syncproto"
)

// advertiseCache memoizes the advertise RESPONSE per namespace for a short TTL.
// The response (changes + refs + change hashes + object bloom) does NOT depend
// on the request's `Have` — the server always advertises everything it holds and
// the CLIENT filters — so it is idempotent per namespace until that namespace is
// written. A burst of pushes/pulls then pays ONE advertise computation, not one
// per call. A short TTL bounds staleness; every write to a namespace clears its
// entry (see invalidateAdvertise), so a client never sees a stale ref set after
// its own push.
type advertiseCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	rows map[int64]advertiseEntry
}

type advertiseEntry struct {
	resp syncproto.AdvertiseResp
	at   time.Time
}

func newAdvertiseCache(ttl time.Duration) *advertiseCache {
	return &advertiseCache{ttl: ttl, rows: map[int64]advertiseEntry{}}
}

func (c *advertiseCache) get(namespaceID int64) (syncproto.AdvertiseResp, bool) {
	if c == nil || c.ttl <= 0 {
		return syncproto.AdvertiseResp{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.rows[namespaceID]
	if !ok || time.Since(e.at) > c.ttl {
		return syncproto.AdvertiseResp{}, false
	}
	return e.resp, true
}

func (c *advertiseCache) put(namespaceID int64, resp syncproto.AdvertiseResp) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[namespaceID] = advertiseEntry{resp: resp, at: time.Now()}
}

// invalidate drops a namespace's cached advertise (called after any write).
func (c *advertiseCache) invalidate(namespaceID int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rows, namespaceID)
}
