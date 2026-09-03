package rpc

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
)

// RPCPath is the single route this handler serves.
const RPCPath = "/rpc"

// maxBodyBytes caps the request body this handler will read, so a
// malicious or buggy peer cannot exhaust daemon memory with an unbounded
// POST body. 4 MiB comfortably covers any realistic JSON-RPC params
// payload for a local IPC call.
const maxBodyBytes = 4 << 20

// peerCredKey is the context key ConnContext stores peer credentials
// under, and Handler reads them back from, via http.Request.Context() —
// http.Server plumbs the ConnContext-derived context through to every
// request served on that connection.
type peerCredKey struct{}

// peerCred is what ConnContext resolves for one accepted connection: the
// remote UID, and whether resolution succeeded at all (a non-unix conn, or
// a platform this ticket has no peer-credential syscall for, reports
// ok=false — Handler treats that as a 403, fail-closed, never fail-open).
type peerCred struct {
	uid int
	ok  bool
}

// ConnContext is assigned to http.Server.ConnContext by the daemon
// composition root (internal/daemon/daemon.go). It resolves conn's peer
// UID once per accepted connection (cheaper than per-request) and stores it
// in the context every request on that connection inherits.
func ConnContext(ctx context.Context, conn net.Conn) context.Context {
	uid, ok := peerCredFromConn(conn)
	return context.WithValue(ctx, peerCredKey{}, peerCred{uid: uid, ok: ok})
}

// ownerUID is a package var (not a direct os.Getuid() call at check time)
// so handler_test.go can override it to test both the accept and reject
// paths without spawning a real second-UID process.
var ownerUID = osGetuid

// Handler is the JSON-RPC 2.0 http.Handler: UID gate, then Parse, then
// SkewCheck, then Registry.Dispatch, wrapped in ResponseEnvelope.
type Handler struct {
	registry *Registry
}

// NewHandler builds a Handler dispatching through registry. The daemon
// composition root mounts it at RPCPath on the daemon's HTTP mux, per this
// ticket's contract.
func NewHandler(registry *Registry) *Handler {
	return &Handler{registry: registry}
}

// ServeHTTP implements http.Handler. Order, per this ticket's contract:
// UID check (via ConnContext) -> HTTP 403 before the JSON-RPC layer ->
// Parse -> version-skew check -> elevation middleware (inside
// Registry.Dispatch's middleware chain) -> handler dispatch.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != RPCPath || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	cred, _ := r.Context().Value(peerCredKey{}).(peerCred)
	if !cred.ok || cred.uid != ownerUID() {
		http.Error(w, "forbidden: socket peer is not the daemon owner", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeEnvelope(w, http.StatusOK, NewEnvelope(nil, nil, newFramingError(codeParseError, "failed to read request body")))
		return
	}
	if len(body) > maxBodyBytes {
		writeEnvelope(w, http.StatusOK, NewEnvelope(nil, nil, newFramingError(codeInvalidRequest, "request body exceeds maximum size")))
		return
	}

	req, parseErr := Parse(body)
	if parseErr != nil {
		writeEnvelope(w, http.StatusOK, NewEnvelope(nil, nil, parseErr))
		return
	}

	if skewErr := SkewCheck(req.ClientVersion); skewErr != nil {
		writeEnvelope(w, http.StatusOK, NewEnvelope(idOrNil(req), nil, skewErr))
		return
	}

	result, dispatchErr := h.registry.Dispatch(r.Context(), req)
	writeEnvelope(w, http.StatusOK, NewEnvelope(idOrNil(req), result, dispatchErr))
}

// idOrNil returns req.ID as the envelope id, or nil for a notification
// (whose response, per spec, a real transport would not send at all; this
// HTTP transport still returns a body since HTTP requires a response, but
// callers issuing notifications are expected to disregard it).
func idOrNil(req *Request) any {
	if req.IsNotification() || len(req.ID) == 0 {
		return nil
	}
	return json.RawMessage(req.ID)
}

// writeEnvelope marshals env as the HTTP response body. JSON-RPC 2.0 errors
// are reported IN the envelope, not via the HTTP status line (status is
// always 200 for a well-formed HTTP exchange, per the spec's HTTP binding
// convention) — status is a parameter here only so a future
// transport-level failure path (never today) has somewhere to plug in.
func writeEnvelope(w http.ResponseWriter, status int, env *ResponseEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
