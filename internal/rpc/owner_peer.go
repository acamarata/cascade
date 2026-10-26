package rpc

// Purpose: the owner-peer seam (ADR-0006 section 3). A request that claims
// the owner CLI (the Cascade-Client marker, no principal token) is only as
// trustworthy as the process behind the socket: an agent that drops its own
// token can send the same marker. The daemon therefore resolves the socket
// peer's pid once per connection and lets one process-wide guard, installed at
// daemon wiring, refuse owner-CLI requests from processes it recognises as
// agent processes.
// Inputs: the accepted net.Conn (pid via SO_PEERCRED on linux, LOCAL_PEERPID
//   on darwin, plus that process's start time) and each request's headers. On
//   linux the pid is the process that called connect(). On darwin it is the
//   last process to do I/O on the connection, so the daemon reads it at accept
//   and again for every owner-CLI request and judges both.
// Outputs: PeerPIDFrom for the guard and for tests; guardOwnerPeer for
//   guardLocalRequest, which writes the HTTP 403 itself on refusal.
// Constraints: fail closed. A connection the composition root armed with
//   GuardedConnContext refuses every owner-CLI request while no guard is
//   installed, so a daemon that forgets SetOwnerPeerGuard refuses the owner view
//   instead of serving it unchecked. A server that never armed its connections
//   (a test handler, mcp serve --socket, node serve) has no peer pid and is not
//   judged, guard or not. On an armed connection, an owner-CLI request whose
//   peer pid is unknown, whose peer is no longer the process that connected (its
//   pid now names a process with a different start time, or none), or which the
//   guard refuses for any reason, is a 403 KindPermissionDenied whose body never
//   echoes the guard's reason. A request
//   carrying a principal token is never judged by pid, and one carrying both
//   headers is left to admitCaller, which refuses it as conflicting. This is
//   not an OS boundary: a process that left the tree it is judged by (a
//   reparented, environment-cleared, setsid descendant) is not recognised, see
//   docs/security-posture/agent-owner-reach.md.
// SPORT: internal.rpc owner_peer (OwnerPeerGuard, SetOwnerPeerGuard,
//   PeerPIDFrom, GuardedConnContext) [ADD] (P1-AGT-16).

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// OwnerPeerGuard decides whether the process peerPID (the connecting process
// of a local socket) may act as the owner CLI. A nil error admits it; any
// error refuses it. The guard runs once per owner-CLI request, on the
// request's goroutine.
type OwnerPeerGuard func(ctx context.Context, peerPID int) error

// ownerPeerGuard is the installed guard; nil means none.
var ownerPeerGuard atomic.Pointer[OwnerPeerGuard]

// SetOwnerPeerGuard installs g for the whole process. The daemon composition
// root calls it once; a nil guard or a second call panics, because silently
// replacing or dropping the guard would reopen the owner view to agents.
func SetOwnerPeerGuard(g OwnerPeerGuard) {
	if g == nil {
		panic("rpc: SetOwnerPeerGuard: nil guard")
	}
	if !ownerPeerGuard.CompareAndSwap(nil, &g) {
		panic("rpc: SetOwnerPeerGuard: a guard is already installed")
	}
}

// peerIdentity is the connector of one connection: its pid and the start time
// the kernel reported for that pid when the connection was accepted (0 when it
// could not be read). A pid alone can be recycled between accept and the check.
type peerIdentity struct {
	pid   int
	start uint64
}

// peerPIDKey is the context key the connection context stores the identity under.
type peerPIDKey struct{}

// peerNowKey is the context key of a function that asks the kernel, again, who
// the peer of this connection is now. See pidsAdmitted.
type peerNowKey struct{}

// peerStartTime returns the kernel's start time for pid (a value that differs
// for any two processes that ever held the same pid), or ok=false. It is a
// variable so tests can stand in for the OS.
var peerStartTime = osPeerStartTime

// PeerPIDFrom returns the pid of the process that opened the connection the
// request arrived on. It reports false when the platform has no peer-pid
// syscall, the connection is not a unix socket, or the kernel could not name
// a process (for instance a peer in another pid namespace).
func PeerPIDFrom(ctx context.Context) (int, bool) {
	id, ok := ctx.Value(peerPIDKey{}).(peerIdentity)
	return id.pid, ok && id.pid > 0
}

// ctxWithPeerPID stores pid and its start time when pid names a process;
// otherwise ctx is returned unchanged, so an unresolved peer reads as unknown.
func ctxWithPeerPID(ctx context.Context, pid int, start uint64, ok bool) context.Context {
	if !ok || pid <= 0 {
		return ctx
	}
	return context.WithValue(ctx, peerPIDKey{}, peerIdentity{pid: pid, start: start})
}

// peerArmedKey marks a connection whose server armed the owner-peer seam.
type peerArmedKey struct{}

// GuardedConnContext returns an http.Server.ConnContext that runs next (the
// daemon passes rpc.ConnContext) and then records the connecting process: its
// pid and start time, and the mark that this connection is judged by the
// owner-peer guard. The daemon composition root assigns it to the daemon
// socket's server together with SetOwnerPeerGuard. A nil next panics.
func GuardedConnContext(next func(context.Context, net.Conn) context.Context) func(context.Context, net.Conn) context.Context {
	if next == nil {
		panic("rpc: GuardedConnContext: nil next")
	}
	return func(ctx context.Context, conn net.Conn) context.Context {
		ctx = context.WithValue(next(ctx, conn), peerArmedKey{}, true)
		return withPeerPID(ctx, conn)
	}
}

// withPeerPID resolves conn's peer pid and start time for GuardedConnContext.
func withPeerPID(ctx context.Context, conn net.Conn) context.Context {
	pid, ok := peerPIDFromConn(conn)
	if !ok {
		return ctx
	}
	start, _ := peerStartTime(pid) // 0 when unreadable: the guard then refuses
	ctx = context.WithValue(ctx, peerNowKey{}, func() (int, bool) { return peerPIDFromConn(conn) })
	return ctxWithPeerPID(ctx, pid, start, true)
}

// sameConnector reports whether the process now holding id.pid is the one that
// connected: the start time was readable at accept and is unchanged. An exited
// or recycled pid, or an unreadable start time, is not the same connector.
func sameConnector(id peerIdentity) bool {
	if id.start == 0 {
		return false
	}
	now, ok := peerStartTime(id.pid)
	return ok && now == id.start
}

// errOwnerPeerRefused is what an agent process sees. Its text is fixed so the
// guard's own reason (which may name pids or markers) never reaches the peer.
var errOwnerPeerRefused = cascade.New(cascade.KindPermissionDenied, "agent processes may not act as the owner")

// guardOwnerPeer applies the installed guard to r and reports whether the
// request may proceed. It writes the refusal itself (HTTP 403 with a
// KindPermissionDenied envelope) and returns false when it does not. Only an
// armed connection is judged, and there a missing guard refuses: fail closed.
func guardOwnerPeer(w http.ResponseWriter, r *http.Request) bool {
	if !headerPresent(r, principal.HeaderOwnerCLI) || headerPresent(r, principal.HeaderToken) {
		return true
	}
	if armed, _ := r.Context().Value(peerArmedKey{}).(bool); !armed {
		return true
	}
	if g := ownerPeerGuard.Load(); g != nil && pidsAdmitted(r.Context(), *g) {
		return true
	}
	writeEnvelope(w, http.StatusForbidden, NewEnvelope(nil, nil, accessErrorObject(errOwnerPeerRefused)))
	return false
}

// pidsAdmitted reports whether the guard admits the peer of this request. The
// pid recorded at accept must still be the process that connected (same start
// time) and pass the guard. Then the kernel is asked again: where it reports a
// different pid now (darwin: the last process to do I/O on the connection), that
// pid must pass the guard too, and a failed second lookup refuses.
func pidsAdmitted(ctx context.Context, g OwnerPeerGuard) bool {
	pid, ok := PeerPIDFrom(ctx)
	if !ok {
		return false
	}
	id, _ := ctx.Value(peerPIDKey{}).(peerIdentity) // present: PeerPIDFrom just read it
	// The start time is read again after the guard: the guard reads the peer's
	// /proc or sysctl entries, and a pid recycled in between must not be judged.
	if !sameConnector(id) || g(ctx, pid) != nil || !sameConnector(id) {
		return false
	}
	now, ok := ctx.Value(peerNowKey{}).(func() (int, bool))
	if !ok {
		return true
	}
	last, ok := now()
	if !ok || last <= 0 {
		return false
	}
	if last == pid {
		return true
	}
	// A different pid is judged with the same bracket: its start time must be
	// readable and unchanged across the guard.
	start, ok := peerStartTime(last)
	return ok && g(ctx, last) == nil && sameConnector(peerIdentity{pid: last, start: start})
}
