// Purpose: guardLocalRequest, the single gate both local-socket routes
//   (POST /rpc and GET /events) run through before any JSON-RPC parse,
//   SSE subscription or handshake — R-14.312: GET /events used to skip the
//   owner-UID check entirely, and neither route rejected a browser-shaped
//   request reaching the socket via a loopback TCP bridge or a
//   DNS-rebinding page.
// Inputs: an in-flight http.Request whose context carries the peerCred
//   ConnContext resolved for this connection (see handler.go), plus its
//   own Host/Origin/Sec-Fetch-*/Content-Type headers.
// Outputs: true when the request may proceed; false (having already
//   written a fixed HTTP 403 body) when refused. The body never echoes
//   any header value back to the caller.
// Constraints: never re-derives the owner check — it reads the same
//   peerCred ConnContext already resolved (the one seam that can mint a
//   real peer UID), it does not call the syscall itself. Fail-closed:
//   an unresolved peer, a non-owner peer, or any browser marker refuses.
// SPORT: internal.rpc request_guard (guardLocalRequest, socketHosts) [ADD]
// (P1-E04-W6-S146-T1).

package rpc

import (
	"mime"
	"net/http"
)

// socketHosts is the exact set of Host header values a first-party local
// client sends today: "unix" (internal/client/client.go, run_exec.go,
// daemon_events_dial.go, plugins/pbd/dispatch_cmd.go,
// internal/nodes/heartbeat.go's tunnel sender) and "cascade.sock" (the
// hook-pack curl commands, internal/fleet/hookpacks). "localhost" is
// deliberately absent: a browser reaching a loopback TCP bridge sends
// Host "localhost:<port>", and no first-party client sends it.
var socketHosts = map[string]bool{
	"unix":         true,
	"cascade.sock": true,
}

// guardLocalRequest runs, in order: (1) the owner-peer check — the same
// ConnContext-derived peerCred Handler.ServeHTTP checked inline before
// this ticket, reused here rather than re-derived; (2) the browser-shaped
// refusal (browserShaped below). It writes the HTTP 403 response itself
// on refusal and returns false; callers must return immediately without
// reading the body, parsing JSON-RPC, or subscribing to the event bus.
func guardLocalRequest(w http.ResponseWriter, r *http.Request) bool {
	cred, _ := r.Context().Value(peerCredKey{}).(peerCred)
	if !cred.ok || cred.uid != ownerUID() {
		http.Error(w, "forbidden: socket peer is not the daemon owner", http.StatusForbidden)
		return false
	}
	if browserShaped(r) {
		http.Error(w, "forbidden: browser-shaped request refused", http.StatusForbidden)
		return false
	}
	return true
}

// headerPresent reports whether r carries key at all, regardless of its
// value — including an empty value ("Origin:\r\n" with nothing after the
// colon), which a bare r.Header.Get(key) != "" check cannot distinguish
// from the header being absent altogether. r.Header is already keyed by
// http.CanonicalHeaderKey (net/http canonicalizes every header it parses
// off the wire before populating the map), so a canonical key here always
// matches what a real request produced.
func headerPresent(r *http.Request, key string) bool {
	_, ok := r.Header[http.CanonicalHeaderKey(key)]
	return ok
}

// browserShaped reports whether r carries any marker of a request that
// reached this local unix socket from a browser — via a loopback TCP
// bridge (ssh -L, socat) or a DNS-rebinding page — rather than from a
// first-party client dialing the socket directly. Any ONE marker refuses:
// an Origin header present at all, with any value including empty
// (browsers never send an empty Origin, but the contract's "present (any
// value)" rule refuses it anyway — see headerPresent) or the literal
// string "null" (which browsers send for opaque origins); a Sec-Fetch-Site
// or Sec-Fetch-Mode header present at all, any value including empty
// (browsers send these on same-origin and no-cors requests too, where
// Origin itself can be absent); a Host that is not exactly one of
// socketHosts; or, for a POST, a Content-Type that does not parse to media
// type "application/json" (absent, unparsable, text/plain, form and
// multipart all refuse — a parameter such as charset=utf-8 is accepted,
// since mime.ParseMediaType reports only the media type itself).
func browserShaped(r *http.Request) bool {
	if headerPresent(r, "Origin") {
		return true
	}
	if headerPresent(r, "Sec-Fetch-Site") || headerPresent(r, "Sec-Fetch-Mode") {
		return true
	}
	if !socketHosts[r.Host] {
		return true
	}
	if r.Method == http.MethodPost {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			return true
		}
	}
	return false
}
