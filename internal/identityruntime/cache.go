package identityruntime

import (
	"container/list"
	"sync"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
)

// defaultCacheSize bounds how many providers are held at once.
//
// 64 because the thing being bounded is workspaces-in-active-use, not
// workspaces-that-exist: an installation with a thousand workspaces still only
// has traffic to a handful at any moment, and the entries are small (an HTTP
// client, four strings, and one token). The bound exists so that a pathological
// access pattern costs evictions instead of memory, not because 64 is a
// capacity anyone should plan against.
const defaultCacheSize = 64

// providerCache is a bounded LRU of providers keyed by connection identity plus
// configuration generation.
//
// ─── Why there is a cache at all ────────────────────────────────────────────
//
// Not for the allocation. identitykc.NewProvider performs no network I/O:
// it validates four strings, picks an http.Client and concatenates two URLs.
// Building one per request would be free, and the resolver would be simpler
// without this file. TestProviderConstructionPerformsNoNetworkIO pins that
// claim so the reasoning can be re-checked rather than believed.
//
// The cache exists for the token. Each provider owns a service-account token
// acquired lazily on its first call and reused until it expires — a full
// client_credentials round trip to Keycloak. Discarding the provider discards
// that token, so an uncached resolver would mint a fresh one on EVERY
// workspace-scoped request: an extra round trip per request, and a session
// per request accumulating in Keycloak.
//
// So the unit of caching is the thing holding the token, and the key is the
// exact configuration that token was minted from. See cacheKey.
//
// ─── What it must never do ──────────────────────────────────────────────────
//
// Serve a provider built from a credential that is no longer current. Every
// event that changes what a workspace should route through changes the key:
//
//	secret rotated in place  → connections.updated_at moves  → new key
//	base URL / realm edited  → connections.updated_at moves  → new key
//	another connection activated → different connection id   → new key
//	connection retired       → no active connection at all   → not resolved
//
// None of those requires an invalidation call, a subscription, or a TTL. The
// resolver reads the connection row on every request — an indexed single-row
// read — and the row it reads IS the invalidation signal. Correct isolation
// was worth more here than saving that read.
type providerCache struct {
	// enabled is false when the resolver was built with a negative CacheSize.
	// Its own tests use that to prove the uncached path resolves identically,
	// which is what makes the cache an optimization rather than a dependency.
	enabled bool
	max     int

	mu    sync.Mutex
	order *list.List               // front = most recently used; values are entry
	index map[string]*list.Element // cache key → its element
}

type entry struct {
	key          string
	connectionID string
	provider     identity.IdentityProvider
}

func newProviderCache(size int) *providerCache {
	if size < 0 {
		return &providerCache{enabled: false}
	}
	if size == 0 {
		size = defaultCacheSize
	}
	return &providerCache{
		enabled: true,
		max:     size,
		order:   list.New(),
		index:   make(map[string]*list.Element, size),
	}
}

// get returns the cached provider for an exact configuration generation.
func (c *providerCache) get(key string) (identity.IdentityProvider, bool) {
	if !c.enabled {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.index[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*entry).provider, true
}

// put stores a provider, evicting the least recently used entry past the bound.
//
// It also drops any OTHER entry for the same connection id. Those are previous
// generations of a connection that has just been reconfigured: they can never
// be looked up again, since nothing will produce their key a second time, and
// leaving them alive would keep a provider holding a superseded credential —
// and its still-valid Keycloak token — in memory until eviction happened to
// reach it. Rotation should release the old session promptly, not eventually.
func (c *providerCache) put(connectionID, key string, p identity.IdentityProvider) {
	if !c.enabled {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.index[key]; ok {
		el.Value.(*entry).provider = p
		c.order.MoveToFront(el)
		return
	}

	for el := c.order.Front(); el != nil; {
		next := el.Next()
		if e := el.Value.(*entry); e.connectionID == connectionID {
			delete(c.index, e.key)
			c.order.Remove(el)
		}
		el = next
	}

	c.index[key] = c.order.PushFront(&entry{key: key, connectionID: connectionID, provider: p})

	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		delete(c.index, oldest.Value.(*entry).key)
		c.order.Remove(oldest)
	}
}

// len reports how many providers are held. Test-facing.
func (c *providerCache) len() int {
	if !c.enabled {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
