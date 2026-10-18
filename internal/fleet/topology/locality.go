// Purpose: the one locality classifier. URLIsLocal decides whether a
//
//	provider base URL names this machine; the conductor's local-only router
//	filter (computedLocalityIsLocal) delegates to it. EndpointIsLocal, the
//	locality input of the data-class ceiling (P1-TOP-02), decides the same
//	question for a runtime profile's endpoint by handing the endpoint to
//	URLIsLocal, so the lane ceiling and the router table cannot disagree.
//	A spawned CLI runtime declares no endpoint and sends every prompt to
//	its vendor, so an empty endpoint is REMOTE on every profile.
//
// Inputs: a base URL string, or one RuntimeProfile. Outputs: a bool.
// Constraints: no "net" import (egress-firewall-gated; this computes, it
//
//	never opens a socket); the endpoint decision reads Endpoint.Host only --
//	never the runtime kind, the profile id or a vendor name; fail-closed
//	(anything not provably local is remote). Canonical spellings only:
//	a spelling Go's HTTP proxy selection would not treat as loopback is
//	never local (see canonicalHost).
//
// SPORT: fleet/topology/locality/ADD (P1-TOP-01).

package topology

import (
	"net/url"
	"path"
	"strings"
	"unicode"
)

// unixEndpointPrefix marks a unix-socket endpoint host ("unix:/path").
const unixEndpointPrefix = "unix:"

// EndpointIsLocal reports whether p's declared endpoint is this machine: a
// unix socket ("unix:<abs path>" or "unix://<abs path>") or a bare host
// that URLIsLocal reads as loopback. An empty host is remote.
func EndpointIsLocal(p RuntimeProfile) bool {
	return hostIsLocal(p.Endpoint.Host)
}

// hostIsLocal classifies a bare endpoint host as the URL endpointURL
// wraps it in; a host that does not wrap cleanly is remote.
func hostIsLocal(host string) bool {
	raw, ok := endpointURL(host)
	return ok && URLIsLocal(raw)
}

// endpointURL wraps a bare endpoint host in the URL the router would see:
// a unix value is already one; any other host becomes "http://<host>",
// with a host holding ':' (an IPv6 literal) bracketed. The wrapped URL must
// parse back to exactly that authority, so a host that smuggles a path,
// query, fragment or userinfo ("127.0.0.1#x", "127.0.0.1@evil.com",
// "localhost/x") reports false.
func endpointURL(host string) (string, bool) {
	if strings.HasPrefix(host, unixEndpointPrefix) {
		return host, true
	}
	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	u, err := url.Parse("http://" + authority)
	if err != nil || u.Host != authority {
		return "", false
	}
	return "http://" + authority, true
}

// URLIsLocal reports whether raw, a provider base URL, names this machine:
// "unix://<abs path>" or "unix:<abs path>" (see isUnixSocketURL), or a URL
// whose canonical host is "localhost", a bracketed "[::1]" or a literal
// 127.0.0.0/8 address. An empty, unparseable, schemeless or hostless URL
// is not local.
func URLIsLocal(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return false
	}
	if u.Scheme == "unix" {
		return isUnixSocketURL(raw, u)
	}
	host, ok := canonicalHost(u.Host)
	if !ok {
		return false
	}
	return host == "localhost" || host == "::1" || isLoopbackIPv4Literal(host)
}

// isUnixSocketURL reports whether raw (parsed as u, scheme "unix") names a
// socket on THIS machine: "unix://" or "unix:" followed by an absolute,
// already clean path (path.Clean leaves it unchanged, so no "//", "." or
// ".." segments) that is more than the root and whose DECODED form holds no
// Unicode whitespace or control character ("%20", "%0b", "%C2%A0" and "%00"
// count). No query or fragment. A unix URL that names a host
// (unix://api.evil.com/x, even unix://localhost/x) or userinfo (unix://@/x)
// says the socket lives elsewhere, so it is never local; a relative path
// ("unix:x.sock") is refused too, since the resolver, not the string, would
// place it.
func isUnixSocketURL(raw string, u *url.URL) bool {
	if !strings.HasPrefix(raw, "unix:/") || strings.ContainsAny(raw, "?#") || u.Host != "" || u.User != nil {
		return false
	}
	p := u.Path
	if strings.IndexFunc(p, isSpaceOrControl) >= 0 {
		return false
	}
	return strings.HasPrefix(p, "/") && p != "/" && path.Clean(p) == p
}

// isSpaceOrControl reports whether r is Unicode whitespace or a control
// character, neither of which belongs in a socket path.
func isSpaceOrControl(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}

// canonicalHost splits u.Host into a host and an optional all-digit port and
// refuses every spelling that is not canonical: an IPv6 literal must be
// bracketed (an unbracketed host holding ':' is refused), and a port, when
// ":" is present, must be a canonical decimal 0-65535 (non-empty, no leading
// zero). Go's HTTP proxy selection reads "http://127.000.000.001" (and, where net/url accepts
// it, a bare "http://::1:") as hosts that are NOT loopback and sends the
// request to HTTP_PROXY, so a classifier that is more lenient than the
// transport would call a request that leaves this machine local. Do not
// lowercase the host to be friendlier to "LOCALHOST": Go's proxy rule skips
// only the lowercase spelling, so lowercasing here would reopen the same
// leak.
func canonicalHost(hostport string) (string, bool) {
	host, rest := hostport, ""
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end < 0 {
			return "", false
		}
		host, rest = hostport[1:end], hostport[end+1:]
	} else if i := strings.LastIndex(hostport, ":"); i >= 0 {
		host, rest = hostport[:i], hostport[i:]
	}
	if rest != "" && (rest[0] != ':' || !canonicalPort(rest[1:])) {
		return "", false
	}
	if !strings.HasPrefix(hostport, "[") && strings.Contains(host, ":") {
		return "", false
	}
	return host, host != ""
}

// canonicalPort reports whether s is a decimal port 0-65535 with no
// leading zero ("00080" and "65536" are refused).
func canonicalPort(s string) bool {
	if len(s) == 0 || len(s) > 5 || (len(s) > 1 && s[0] == '0') {
		return false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
	}
	return n <= 65535
}

// isLoopbackIPv4Literal reports whether host is a dotted-quad address in
// 127.0.0.0/8, and ONLY a literal address.
//
// A bare `HasPrefix(host, "127.")` admits `127.evil.com` — a registrable
// domain whose Hostname() starts with "127." and which resolves wherever its
// owner points it. A provider record with that BaseURL would be classified
// controller-local, and every gate built on this predicate would wave it
// through. Found by the independent review of P1-E20-W5-S44-T2.
//
// The host must be exactly four canonical decimal octets (no leading
// zeros), each 0-255, the first 127, which separates the literal address
// from any domain.
func isLoopbackIPv4Literal(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) != 4 || parts[0] != "127" {
		return false
	}
	for _, part := range parts {
		// Canonical decimal only: "127.000.000.001" parses as loopback to a
		// lenient reader, but Go's HTTP proxy selection does not treat that
		// spelling as loopback and would send the request to the configured
		// proxy, i.e. off this machine.
		if len(part) == 0 || len(part) > 3 || (len(part) > 1 && part[0] == '0') {
			return false
		}
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
			n = n*10 + int(r-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}
