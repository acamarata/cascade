// Purpose: CheckHTTP, the net-scope check every host_http_request call
//
//	transits before the host performs the outbound request.
//
// Inputs: the target URL a plugin asked the host to fetch.
// Outputs: nil when the URL's host is covered by the plugin's declared
//
//	net scopes; a denied, audited error otherwise.
//
// Constraints: fail closed — an unparseable URL, an empty host, and an
//
//	empty scope list all deny. The scope-matching algorithm mirrors
//	internal/plugins/wasm/host_abi_net.go's checkNetScope exactly (bare
//	hostname exact match, "*.example.com" subdomain match); this package
//	cannot import that one (both are under internal/plugins/**, see
//	capability.go's doc comment), so the two implementations are
//	intentionally parallel rather than shared.
//
// SPORT: internal/plugins/host http-scope-check (ADD) — P1-E15-W4-S31-T4.

package host

import (
	"context"
	"net/url"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// httpCallType names this check in audit entries and denial messages.
const httpCallType = "http"

// CheckHTTP reports whether rawURL's host is covered by e's declared net
// scopes. A malformed URL, a URL with no host, and an empty scope list
// all deny (fail closed); a covered host allows with no audit entry
// (allowed HTTP calls are the common case, matching CheckStorage's
// silence on success — see capability.go's allow doc comment).
func (e *HostBoundaryEnforcer) CheckHTTP(ctx context.Context, rawURL string) error {
	host, err := hostnameOf(rawURL)
	if err != nil {
		return e.Deny(ctx, httpCallType, err)
	}
	if !netScopeCovers(e.grants.NetScopes, host) {
		return e.Deny(ctx, httpCallType,
			cascade.Newf(cascade.KindCapabilityDenied, "host_http_request: %q is outside the plugin's declared net scope", host))
	}
	return nil
}

// hostnameOf extracts and lowercases rawURL's hostname, failing closed on
// a parse error or an empty result.
func hostnameOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindInvalidInput, err, "host_http_request: %q is not a well-formed URL", rawURL)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", cascade.Newf(cascade.KindInvalidInput, "host_http_request: %q has no host", rawURL)
	}
	return host, nil
}

// netScopeCovers reports whether host is covered by any scope in scopes.
// A nil or empty scopes list covers nothing.
func netScopeCovers(scopes []string, host string) bool {
	for _, scope := range scopes {
		if scopeMatches(strings.ToLower(scope), host) {
			return true
		}
	}
	return false
}

// scopeMatches reports whether host is covered by one declared scope: a
// bare hostname matches exactly; "*.example.com" matches any subdomain of
// example.com but not example.com itself.
func scopeMatches(scope, host string) bool {
	if scope == "" {
		return false
	}
	if strings.HasPrefix(scope, "*.") {
		suffix := scope[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".")
	}
	return scope == host
}
