// Purpose: R-21.29's 429/401/403/5xx error semantics, typed on the frozen
//
//	pkg/cascade error kinds (06 Sec.2). Each kind acts on a DIFFERENT
//	entity -- scope, credential or domain -- so a caller can react
//	correctly without three copies of the same switch: 429 constrains the
//	SCOPE (R-21.95, never a sibling key or a sibling domain sharing it),
//	401 quarantines the CREDENTIAL only (a sibling key stays selectable),
//	403 quarantines the DOMAIN, and 5xx/timeout backs off and reroutes.
//
// Inputs: a DomainID/CredentialID pair and the provider error. Outputs: a
//
//	Retry decision.
//
// Constraints: no bare time.Now/rand outside tests (Art.7.3) -- the
//
//	decorrelated backoff draws from the Chooser's seeded rand.Source.
//
// SPORT: fleet/topology/chooser_errors/ADD (P1-E40-W9-S78-T1).

package topology

import (
	"context"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// defaultRateLimitProbe is R-21.29's fallback when a 429 carries neither a
// Retry-After header nor a bucket reset_at.
const defaultRateLimitProbe = 60 * time.Second

// maxBackoffAttempts is R-21.29's 5xx/timeout attempt ceiling before
// OnProviderError reroutes instead of retrying the same domain.
const maxBackoffAttempts = 5

// maxBackoffDelay caps the decorrelated backoff at 60s.
const maxBackoffDelay = 60 * time.Second

// maxRetryAfterSeconds bounds a delta-seconds Retry-After value to what
// time.Duration can hold without overflowing on multiplication by
// time.Second; a provider value beyond one year is refused rather than
// silently wrapped to a negative duration (FuzzParseRetryAfter's own
// discovered failure mode before this bound existed).
const maxRetryAfterSeconds = int64(365 * 24 * 3600)

// Retry is OnProviderError's decision: wait After before the next attempt
// (zero means retry immediately), and whether the caller must re-rank
// (Reroute) rather than retry the same domain/credential.
type Retry struct {
	After   time.Duration
	Reroute bool
	// AffectedDomains lists every domain the transition disabled, for a
	// caller-facing explain surface (S-78.T3's `fleet lanes explain`).
	// Populated only for the 429/scope path via limit_scope.go's
	// DomainsInScope -- nil for every other Retry.
	AffectedDomains []DomainID
}

// OnProviderError classifies err (one of the frozen pkg/cascade kinds) and
// applies R-21.29's transition for dom/cred, returning the resulting
// Retry decision. An unrecognised kind is ErrTopologyInvariant -- this
// function never guesses a transition for a kind it was not told about.
func (c *Chooser) OnProviderError(ctx context.Context, dom DomainID, cred CredentialID, err error) (Retry, error) {
	kind, ok := cascade.KindOf(err)
	if !ok {
		return Retry{}, newInvariantErr("chooser_errors", string(dom), "error carries no frozen taxonomy kind")
	}
	switch kind { //nolint:exhaustive // R-21.29 defines a transition for exactly these five kinds; every other frozen kind falls to the named default below, which fails closed rather than guessing.
	case cascade.KindQuotaExhausted:
		return c.on429(dom, err)
	case cascade.KindPermissionDenied:
		return c.on401(cred, err)
	case cascade.KindCapabilityDenied, cascade.KindPolicyDenied:
		return c.on403(dom)
	case cascade.KindUnavailable, cascade.KindTimeout:
		return c.on5xx(ctx, dom)
	default:
		return Retry{}, newInvariantErr("chooser_errors", string(dom), "kind "+kind.String()+" has no R-21.29 transition")
	}
}

// on429 marks dom's limit SCOPE constrained (R-21.95) until Retry-After,
// the bucket's reset_at, or a 60s probe, disabling every domain that
// shares the scope and forcing a full re-rank -- never a sibling key and
// never a sibling domain inside the same scope.
func (c *Chooser) on429(dom DomainID, err error) (Retry, error) {
	c.mu.Lock()
	domain, ok := c.domains[dom]
	account := c.accounts[domain.AccountRef]
	c.mu.Unlock()
	if !ok {
		return Retry{}, newNotFoundErr("quota_domain", string(dom))
	}
	scope := ResolveScope(account, domain)
	now := c.clock.Now()
	until := now.Add(defaultRateLimitProbe)
	if raw, ok := extractRetryAfter(errMessage(err)); ok {
		if d, ok := ParseRetryAfter(raw, now); ok {
			until = now.Add(d)
		}
	} else if resetAt, ok := latestResetAt(domain); ok && resetAt.After(now) {
		until = resetAt
	}
	c.mu.Lock()
	c.scopeConstrainedUntil[scope] = until
	domains := make([]QuotaDomain, 0, len(c.domains))
	for _, d := range c.domains {
		domains = append(domains, d)
	}
	accounts := c.accounts
	c.mu.Unlock()
	c.recordEwma429(scope, 1)
	affected := DomainsInScope(scope, domains, accounts)
	sort.Slice(affected, func(i, j int) bool { return affected[i] < affected[j] })
	return Retry{After: until.Sub(now), Reroute: true, AffectedDomains: affected}, nil
}

// on401 quarantines cred only -- a sibling credential in the same domain
// remains a legitimate next choice. Cleared only when `cascade provider
// test` succeeds (a future ticket's write path; this function only sets
// the quarantine).
func (c *Chooser) on401(cred CredentialID, _ error) (Retry, error) {
	if cred == "" {
		return Retry{}, newInvariantErr("chooser_errors", "", "401 requires a credential id")
	}
	c.mu.Lock()
	c.credOverrides[cred] = credOverride{quarantined: true, reason: "invalid credential (401)"}
	c.mu.Unlock()
	return Retry{Reroute: false}, nil
}

// on403 quarantines the DOMAIN pending `doctor --providers`; every lane
// whose domain is quarantined becomes unavailable at the next
// eligibility check (R-21.24 invariant).
func (c *Chooser) on403(dom DomainID) (Retry, error) {
	c.mu.Lock()
	c.domainQuarantined[dom] = true
	c.mu.Unlock()
	return Retry{Reroute: true}, nil
}

// on5xx applies the decorrelated backoff, up to maxBackoffAttempts, then
// reroutes to another domain.
func (c *Chooser) on5xx(_ context.Context, dom DomainID) (Retry, error) {
	c.mu.Lock()
	attempt := c.backoffAttempts[dom]
	c.backoffAttempts[dom] = attempt + 1
	if attempt >= maxBackoffAttempts {
		delete(c.backoffAttempts, dom)
		c.mu.Unlock()
		return Retry{Reroute: true}, nil
	}
	delay := decorrelatedBackoff(attempt, c.rng)
	c.mu.Unlock()
	return Retry{After: delay, Reroute: false}, nil
}

// decorrelatedBackoff computes R-21.29's `min(60s, 1s*2^attempt) *
// U(0.5, 1.5)`, drawing its jitter from r (never a global rand source,
// Art.7.3).
func decorrelatedBackoff(attempt int, r *rand.Rand) time.Duration {
	base := time.Duration(math.Min(float64(maxBackoffDelay), float64(time.Second)*math.Pow(2, float64(attempt))))
	jitter := 0.5 + r.Float64() // U(0.5, 1.5)
	return time.Duration(float64(base) * jitter)
}

// errMessage renders err's message for ParseRetryAfter scanning; a nil
// err yields "".
func errMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// retryAfterMarker is the canonical substring a provider driver embeds in
// a KindQuotaExhausted error's message to surface the wire Retry-After
// value through the frozen (kind, message) error shape -- pkg/cascade
// carries no structured field for it. Absent this marker, on429 falls
// back to the domain's own bucket reset_at, then the 60s probe.
const retryAfterMarker = "retry-after="

// extractRetryAfter finds retryAfterMarker in msg and returns the token
// that follows it up to the next space, or ("", false) when absent.
func extractRetryAfter(msg string) (string, bool) {
	idx := indexOf(msg, retryAfterMarker)
	if idx < 0 {
		return "", false
	}
	rest := msg[idx+len(retryAfterMarker):]
	end := len(rest)
	for i, r := range rest {
		if r == ' ' {
			end = i
			break
		}
	}
	if end == 0 {
		return "", false
	}
	return rest[:end], true
}

// indexOf is a tiny strings.Index wrapper kept local so this file's import
// block stays exactly the packages it needs.
func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// latestResetAt returns the latest ResetAt across dom's real dimensions,
// the R-21.29 fallback when no Retry-After value is present.
func latestResetAt(dom QuotaDomain) (time.Time, bool) {
	var latest time.Time
	found := false
	for _, b := range dom.Dimensions {
		if !found || b.ResetAt.After(latest) {
			latest = b.ResetAt
			found = true
		}
	}
	return latest, found
}

// ParseRetryAfter extracts a Retry-After value from v: either delta-
// seconds ("120") or an HTTP-date (RFC 1123), per RFC 9110 Sec.10.2.3.
// Arbitrary bytes never panic -- see chooser_errors_fuzz_test.go's
// FuzzParseRetryAfter. Returns (0, false) when v carries neither shape.
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 || secs > maxRetryAfterSeconds {
			// A negative value is nonsensical and a value this large would
			// overflow time.Duration on multiplication -- refuse rather
			// than guess (fail closed), never wrap to a negative duration.
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, ok := parseHTTPDate(v); ok {
		d := t.Sub(now)
		if d < 0 {
			return 0, true
		}
		return d, true
	}
	return 0, false
}

// httpDateLayouts are the three HTTP-date layouts RFC 9110 Sec.5.6.7
// recognises, in the order net/http's own ParseTime tries them. Declared
// locally rather than importing net/http, which the default unit lane
// forbids (06 Sec.2) and which internal/fleet/topology is not on the
// egress allowlist for.
var httpDateLayouts = [...]string{
	"Mon, 02 Jan 2006 15:04:05 GMT",  // RFC 1123, preferred
	"Monday, 02-Jan-06 15:04:05 MST", // RFC 850
	"Mon Jan _2 15:04:05 2006",       // ANSI C asctime
}

// parseHTTPDate tries every httpDateLayouts entry, returning the first
// successful parse in UTC.
func parseHTTPDate(v string) (time.Time, bool) {
	for _, layout := range httpDateLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
