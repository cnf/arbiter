package ui

import (
	"fmt"
	"sync"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// discoveryCacheTTL bounds how long a cached ledger result is served before
// the underlying query runs again.
//
// The query result is the same regardless of which caller measures it, so
// caching it (rather than caching per-response HTML) means a cache hit
// benefits both the page and a differently-parameterised repeat with the
// same params. The window is 30 days by default (discoveryDefaultWindow):
// a couple of minutes of staleness cannot meaningfully change an aggregate
// over that span, and Discovery exists to spot week-over-week patterns, not
// react to the last few seconds of traffic. Short enough that "I changed the
// filters and got stale data" is never a believable complaint; long enough
// that reloading the same view twice in a row (the common case — load,
// look, look again) is instant instead of paying the full query again.
//
// This is deliberately the simple version: a TTL, not an invalidate-on-write
// hook. New data arriving does not evict a cached entry early — it just
// isn't reflected until the entry expires. Finer-grained invalidation is
// explicitly deferred; see PICKUP.md.
const discoveryCacheTTL = 2 * time.Minute

// discoveryCacheResult is what's cached: everything RepeatedContent,
// ContentHashCounts, and SessionlessRequestCount compute for one parameter
// combination. Seen/ignored marks (DiscoveryStates) are deliberately NOT
// part of this — that query is cheap (a handful of rows keyed by hash) and
// an operator toggling a mark expects to see it stick immediately, not
// wait out a cache TTL.
type discoveryCacheResult struct {
	blocks      []store.RepeatedContent
	total       int64
	matching    int64
	sessionless int64
}

// discoveryCacheEntry pairs a result with when it was computed, so callers
// can decide for themselves whether it's still fresh enough.
type discoveryCacheEntry struct {
	result   discoveryCacheResult
	cachedAt time.Time
}

// discoveryCache is a process-local, in-memory cache of the Discovery
// ledger's expensive query, keyed by the parameters that change its result.
//
// A single mutex guards a plain map. Arbiter is a single-operator admin
// surface (see project memory: single-user deployment, one browser tab at a
// time in practice) — there is no concurrent-tenant load to shard against,
// and a plain mutex is not a bottleneck for a page loaded a few times a
// minute. It exists only in memory: a restart clears it, which is fine,
// since the first load after a restart pays the real cost once and every
// load after that is cheap again. On-disk persistence across restarts is
// deliberately out of scope for this pass — see PICKUP.md.
type discoveryCache struct {
	mu      sync.Mutex
	entries map[string]discoveryCacheEntry
}

func newDiscoveryCache() *discoveryCache {
	return &discoveryCache{entries: make(map[string]discoveryCacheEntry)}
}

// discoveryCacheKey turns the four query-shaping parameters into a cache
// key. It does not need to be a hash — the string is short and the map
// comparison is exact — so it is built directly from the values rather than
// through a digest, which would only add cost for no benefit at this size.
func discoveryCacheKey(since time.Duration, minRequests, minSessions, limit int) string {
	return fmt.Sprintf("%d|%d|%d|%d", since, minRequests, minSessions, limit)
}

// get returns the cached result for key if one exists and is still within
// discoveryCacheTTL, else ok=false. A stale entry is left in the map rather
// than deleted here — the next successful set overwrites it, and there is
// no separate eviction pass to keep the cache from growing: the key space is
// the four bounded query parameters as exposed by the UI, not user input
// with unbounded cardinality.
func (c *discoveryCache) get(key string) (discoveryCacheResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Since(entry.cachedAt) > discoveryCacheTTL {
		return discoveryCacheResult{}, false
	}
	return entry.result, true
}

// set stores result under key, timestamped now.
func (c *discoveryCache) set(key string, result discoveryCacheResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = discoveryCacheEntry{result: result, cachedAt: time.Now()}
}
