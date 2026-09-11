package wasm

import (
	"context"
	"net/url"
	"strings"

	"github.com/tetratelabs/wazero/api"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: host_http (task 6, second half) — the ONLY WASM outbound
//
//	network path. Every call is checked against the calling plugin's
//	declared net scopes BEFORE NetDoer is ever invoked (fail closed: an
//	empty scope list refuses every URL); the actual transit through the
//	H/S-16.T1 egress inventory's substitution and sensitivity pass is
//	NetDoer's concrete implementation's job (a composition root binds
//	the real internal/hooks/egress.Engine in, mirroring
//	internal/plugins/process/types.go's own EgressInterceptor pattern —
//	this package cannot import internal/hooks/egress directly).

// NetDoer is host_http's delegate: performs the already-scope-checked
// outbound call. Implementations are expected to route through the
// host's egress substitution and sensitivity pass; this package's own
// responsibility ends at the scope check below.
type NetDoer interface {
	Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// ErrNetScopeViolation reports a host_http call whose target host is not
// covered by the calling plugin's declared net scopes.
var ErrNetScopeViolation = cascade.New(cascade.KindPermissionDenied,
	"wasm: host_http: target URL is outside the plugin's declared net scope")

// checkNetScope reports whether rawURL's host is covered by scopes. A
// scope is a bare hostname (exact match) or "*.example.com" (suffix
// match on the last "." + example.com or the base host without the
// leading dot); an empty or unparseable rawURL, and an empty scopes
// list, both fail closed (refused, never treated as "allow").
func checkNetScope(rawURL string, scopes []string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "wasm: host_http: %q is not a well-formed URL", rawURL)
	}
	host := strings.ToLower(u.Hostname())
	for _, scope := range scopes {
		if scopeMatches(strings.ToLower(scope), host) {
			return nil
		}
	}
	return ErrNetScopeViolation
}

// scopeMatches reports whether host is covered by scope, per
// checkNetScope's doc.
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

// hostHTTPFn builds the host_http wazero function bound to cs.
func hostHTTPFn(cs *callState) func(context.Context, api.Module, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
		var req HTTPRequest
		if err := readRequest(m, ptr, length, &req); err != nil {
			return writeErr(m, err)
		}
		if err := checkNetScope(req.URL, cs.netScopes); err != nil {
			return writeErr(m, err)
		}
		if cs.deps.Net == nil {
			return writeErr(m, missingDep(hostFnHTTP))
		}
		resp, err := cs.deps.Net.Do(ctx, req)
		if err != nil {
			return writeErr(m, err)
		}
		return writeOK(m, resp)
	}
}
