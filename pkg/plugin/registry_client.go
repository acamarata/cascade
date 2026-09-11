package plugin

// Purpose: RegistryClient orchestrates RegistryFetcher, RegistryVerifier
//   and RegistryCache into the fetch/verify/cache flow X/S-50.T1
//   implements: cache-hit avoids the network entirely; cache-miss fetches,
//   verifies (never caching an unverified payload), and caches; Search
//   answers a case-insensitive substring query over the verified index.
// Inputs: a RegistryConfig plus the three injected seams at construction;
//   a context and, for Search, a query string on every call.
// Outputs: a verified RegistryIndex or matching []RegistryIndexEntry, or
//   a typed *cascade.Error from registry_errors.go.
// Constraints: SECURITY DEVIATION FROM TICKET PROSE, recorded here and in
//   the journal: the ticket's Fetch behavior step 1 says a cache hit is
//   "unmarshal and return it — no network call, no re-verify (cached copy
//   was verified on write)". This client instead RE-VERIFIES the cached
//   bytes' signature on every cache read (cheap — Ed25519.Verify is local,
//   no network), because the task's supply-chain rules are explicit that
//   "a cache entry must be re-verified or provably trusted on read, and a
//   corrupt cache file must be refused rather than served" — a bare
//   unmarshal-and-trust read is a cache-poisoning hole (an attacker who
//   can write CacheDir/index.json.tmp between writes, or replace the file
//   directly, would otherwise have their tampered content served without
//   any check). Re-verifying preserves the "no network call" half of the
//   contract exactly (TestCacheHit asserts zero fetcher invocations) while
//   closing the poisoning gap. A corrupt or unparseable cache entry is
//   treated as a cache MISS (re-fetch), never served.
//   ROLLBACK/REPLAY GAP: RegistryIndex carries no monotonic
//   version/sequence/timestamp field of its own (only SchemaVersion, which
//   identifies the document SHAPE, not a point in time), so this client
//   cannot detect or refuse an index that is a valid, signed, but OLDER
//   replay of a previous index. This is a real gap, not implemented here,
//   and flagged rather than silently absent — see the journal.
// SPORT: pkg/plugin registry-client (ADD) — P1-E24-W5-S50-T1.

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Clock abstracts time.Now so RegistryClient never reads the wall clock
// directly (02-TARGET-STRUCTURE.md §v1.1's clock-injection rule). This
// package defines no production implementation of its own — pkg/plugin
// never imports internal/, and the tree's one sanctioned bare-time.Now()
// site is internal/runtime.SystemClock (linter-exempted there only); the
// composition root duck-types that value (or any other Now() time.Time
// type) in as a Clock here, and NewRegistryClient requires one rather
// than defaulting to a hidden real-clock implementation.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// RegistryConfig configures a RegistryClient.
type RegistryConfig struct {
	// RegistryURL is the registry root; interpretation (path joining) is
	// the injected RegistryFetcher's, not this client's.
	RegistryURL string
	// PublicKey is the registry's Ed25519 public key material, passed
	// through to the injected RegistryVerifier's construction by the
	// caller — RegistryConfig carries it for callers that build both from
	// one config source (08-INIT-CONFIG-SPEC §3 [registry]).
	PublicKey []byte
	// CacheDir is the writable directory the cache is stored under.
	CacheDir string
	// CacheTTL is how long a cached index is served without a network
	// fetch. Zero means every Fetch call re-fetches.
	CacheTTL time.Duration
}

const registryCacheKey = "index"

// cachedIndexEnvelope is what RegistryClient stores in the cache: the
// verified raw index bytes plus the instant they were cached, so a later
// Fetch can decide cache-hit vs cache-miss against CacheTTL without
// touching the network.
type cachedIndexEnvelope struct {
	CachedAt time.Time `json:"cached_at"`
	Raw      []byte    `json:"raw"`
}

// RegistryClient is the production fetch/verify/cache orchestrator.
type RegistryClient struct {
	cfg      RegistryConfig
	fetcher  RegistryFetcher
	verifier RegistryVerifier
	cache    RegistryCache
	clock    Clock
}

// NewRegistryClient constructs a RegistryClient over the given injected
// seams. cache may be nil, in which case every Fetch call re-fetches and
// re-verifies with no caching at all. clock must be non-nil in a
// production caller (the composition root supplies its own Clock, e.g.
// internal/runtime.SystemClock, which duck-types this interface); a nil
// clock makes cache-hit lookups panic rather than silently reading the
// real wall clock, which is the deliberately loud failure mode for a
// construction bug in composition-root wiring.
func NewRegistryClient(cfg RegistryConfig, fetcher RegistryFetcher, verifier RegistryVerifier, cache RegistryCache, clock Clock) *RegistryClient {
	return &RegistryClient{cfg: cfg, fetcher: fetcher, verifier: verifier, cache: cache, clock: clock}
}

// WithClock overrides the client's time source. Exported for tests that
// need a deterministic Clock (Art.7 — no bare time.Now in domain logic).
func (c *RegistryClient) WithClock(clock Clock) *RegistryClient {
	c.clock = clock
	return c
}

// Fetch returns the current verified RegistryIndex, serving a fresh cache
// entry without any network call, or fetching, verifying, and caching a
// new one otherwise. Fetch never returns the result of an unverified
// payload — a verification failure is returned as an error and nothing is
// cached.
func (c *RegistryClient) Fetch(ctx context.Context) (RegistryIndex, error) {
	if c.cache != nil {
		if idx, ok := c.cacheHit(ctx); ok {
			return idx, nil
		}
	}

	data, err := c.fetcher.FetchIndex(ctx)
	if err != nil {
		return RegistryIndex{}, err
	}
	idx, err := c.verifier.VerifyIndex(ctx, data)
	if err != nil {
		// Fail closed: the payload is never cached and never returned.
		return RegistryIndex{}, err
	}

	if c.cache != nil {
		c.writeCache(ctx, data)
	}
	return idx, nil
}

// cacheHit reports whether a fresh, verifiable cache entry exists. Any
// failure — cache read error, corrupt envelope, expired TTL, or a
// signature that no longer verifies — is treated as a miss, never as a
// served-anyway result.
func (c *RegistryClient) cacheHit(ctx context.Context) (RegistryIndex, bool) {
	raw, ok, err := c.cache.Get(ctx, registryCacheKey)
	if err != nil || !ok {
		return RegistryIndex{}, false
	}
	var env cachedIndexEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return RegistryIndex{}, false
	}
	if c.clock.Now().Sub(env.CachedAt) > c.cfg.CacheTTL {
		return RegistryIndex{}, false
	}
	idx, err := c.verifier.VerifyIndex(ctx, env.Raw)
	if err != nil {
		return RegistryIndex{}, false
	}
	return idx, true
}

func (c *RegistryClient) writeCache(ctx context.Context, verifiedRaw []byte) {
	env := cachedIndexEnvelope{CachedAt: c.clock.Now(), Raw: verifiedRaw}
	raw, err := json.Marshal(env)
	if err != nil {
		return
	}
	_ = c.cache.Put(ctx, registryCacheKey, raw)
}

// Search returns every entry in the current verified index whose Name,
// Description, or any Tag contains q as a case-insensitive substring. An
// empty q returns every entry (browse mode).
func (c *RegistryClient) Search(ctx context.Context, q string) ([]RegistryIndexEntry, error) {
	idx, err := c.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	if q == "" {
		return idx.Entries, nil
	}
	needle := strings.ToLower(q)
	var out []RegistryIndexEntry
	for _, e := range idx.Entries {
		if strings.Contains(strings.ToLower(e.Name), needle) ||
			strings.Contains(strings.ToLower(e.Description), needle) ||
			entryTagMatches(e.Tags, needle) {
			out = append(out, e)
		}
	}
	return out, nil
}

func entryTagMatches(tags []string, needle string) bool {
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), needle) {
			return true
		}
	}
	return false
}
