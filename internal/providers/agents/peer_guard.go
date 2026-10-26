package agents

// Purpose: AgentPeerGuard, the rpc.OwnerPeerGuard that stops a live agent
// spawn's process tree from acting as the owner CLI on the daemon socket
// (ADR-0006 section 3, R16 M-2). An agent that runs
// `env -u CASCADE_PRINCIPAL_TOKEN cascade ...` sends the owner-CLI marker with
// no token; this guard recognises the connecting process as the spawn's own.
// Inputs: the socket peer's pid (rpc.PeerPIDFrom) and a live-spawn source
//   (the delegation service's list of running spawns).
// Outputs: nil admits the peer; any error refuses it.
// Constraints: fail closed. A peer is refused when ANY of these holds: its
//   environment holds CASCADE_LEASE_ID=<a live marker>; it or an ancestor within
//   64 hops is a live spawn's pid or is in a live spawn's process group or
//   session. A peer that cannot be inspected (gone, environment unreadable,
//   chain too deep, no inspector on this OS) is refused, never admitted; the
//   environment is read for every peer once any spawn is live, whether or not a
//   marker is registered yet. A peer is admitted only when every check ran and
//   found nothing. With no live spawn nothing can match and the peer is
//   admitted without inspection; any live spawn that carries no usable identifier
//   (no marker, pid or group) refuses every peer, since it cannot be matched.
//   The guard narrows reach, it is not isolation: a process that left the tree (double fork, environment cleared, setsid,
//   reparented to init or launchd) matches none of the checks, see
//   docs/security-posture/agent-owner-reach.md (DEF-P2-agent-owner-reach).
//   The daemon's live-spawn source is one SpawnRegistry the composition root
//   owns; whatever starts an agent spawn registers it before the spawn runs.
// SPORT: internal.providers.agents peer_guard (AgentPeerGuard, LiveSpawn,
//   SpawnRegistry) [ADD] (P1-AGT-16).

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// maxPeerHops bounds the ppid walk; a longer chain is refused, not admitted.
const maxPeerHops = 64

// errPeerIsAgent is the refusal for a peer inside a live spawn's tree. The
// caller never sees this text (rpc writes its own fixed body).
var errPeerIsAgent = cascade.New(cascade.KindPermissionDenied, "agents: the socket peer belongs to a live agent spawn")

// LiveSpawn identifies one running agent spawn. Marker is its lease marker
// (the CASCADE_LEASE_ID value in its environment); PGID is its process group
// and PID its leader's pid. A field that is not known yet is zero (or a
// marker that is not a NewLeaseMarker value) and is not matched on, so a spawn
// is covered by its marker from the moment it is registered, before its pid
// exists. A spawn must stay in the live list until its lease sweep has returned
// nil: a descendant that outlives the leader still carries the marker, and a
// spawn dropped earlier is no longer matched.
type LiveSpawn struct {
	Marker string
	PGID   int
	PID    int
}

// peerInspector is the per-OS view of one process. Every method returns an
// error for a process it cannot see, which the guard treats as a refusal.
type peerInspector interface {
	// holds reports whether pid's environment holds any of entries as a whole
	// KEY=VALUE entry. Unreadable (denied, gone) is an error, not false.
	holds(pid int, entries []string) (bool, error)
	// groups returns pid's process group and session ids.
	groups(pid int) (pgid, sid int, err error)
	// parent returns pid's parent pid.
	parent(pid int) (int, error)
}

// peerGuard is one guard's live-spawn source and OS view.
type peerGuard struct {
	live func() []LiveSpawn
	insp peerInspector
}

// AgentPeerGuard builds the guard for rpc.SetOwnerPeerGuard. live is called
// once per owner-CLI request and must return the spawns running now. On an OS
// with no inspector the guard refuses every peer. A nil live panics: it is a
// wiring error and a silent default would admit agents.
func AgentPeerGuard(live func() []LiveSpawn) rpc.OwnerPeerGuard {
	if live == nil {
		panic("agents: AgentPeerGuard: nil live-spawn source")
	}
	insp, err := platformPeerInspector()
	if err != nil {
		return func(context.Context, int) error { return refuse(err, "no process inspector on this platform") }
	}
	return peerGuard{live: live, insp: insp}.check
}

// refuse wraps an inspection failure as the guard's refusal.
func refuse(err error, what string) error {
	return cascade.Wrap(cascade.KindPermissionDenied, err, "agents: the socket peer could not be cleared: "+what)
}

// liveSet is the identifiers one check matches a peer against.
type liveSet struct {
	entries []string
	pgids   map[int]bool
	pids    map[int]bool
	// unmatchable is set when any live spawn carries no usable identifier.
	unmatchable bool
}

// newLiveSet keeps only identifiers that can match: a marker of the exact
// NewLeaseMarker shape, and ids above 1 (0 is unknown; 1 is init or launchd,
// every chain's end). A spawn that adds no identifier at all is flagged
// unmatchable: the guard cannot tell its tree from anyone else's.
func newLiveSet(spawns []LiveSpawn) liveSet {
	s := liveSet{pgids: map[int]bool{}, pids: map[int]bool{}}
	for _, sp := range spawns {
		usable := false
		if validMarker(sp.Marker) {
			s.entries = append(s.entries, leaseEntry(sp.Marker))
			usable = true
		}
		if sp.PGID > 1 {
			s.pgids[sp.PGID] = true
			usable = true
		}
		if sp.PID > 1 {
			s.pids[sp.PID] = true
			usable = true
		}
		s.unmatchable = s.unmatchable || !usable
	}
	return s
}

// check is the rpc.OwnerPeerGuard. Once any spawn is live the peer's
// environment is always read, even when no marker is registered yet (only pid
// or group known): an environment that cannot be read refuses the peer.
func (g peerGuard) check(ctx context.Context, pid int) error {
	if pid <= 1 {
		return refuse(cascade.New(cascade.KindInvalidInput, "agents: not a process id"), "invalid peer pid")
	}
	spawns := g.live()
	set := newLiveSet(spawns)
	if len(spawns) == 0 {
		return nil
	}
	if set.unmatchable {
		return refuse(cascade.New(cascade.KindPermissionDenied, "agents: a live spawn has no usable identifier"), "live spawn cannot be matched")
	}
	if err := ctx.Err(); err != nil {
		return refuse(err, "request canceled")
	}
	held, err := g.insp.holds(pid, set.entries)
	if err != nil {
		return refuse(err, "environment unreadable")
	}
	if held {
		return errPeerIsAgent
	}
	if len(set.pgids) == 0 && len(set.pids) == 0 {
		return nil
	}
	return g.checkTree(pid, set)
}

// checkTree refuses a peer, or any of up to maxPeerHops ancestors, that is a
// live spawn's pid or sits in a live spawn's process group or session. A chain
// that is still going after that is refused, not admitted.
func (g peerGuard) checkTree(pid int, set liveSet) error {
	cur := pid
	for hop := 0; hop <= maxPeerHops; hop++ {
		if set.pids[cur] {
			return errPeerIsAgent
		}
		pgid, sid, err := g.insp.groups(cur)
		if err != nil {
			return refuse(err, "process group unreadable")
		}
		if set.pgids[pgid] || set.pgids[sid] {
			return errPeerIsAgent
		}
		parent, err := g.insp.parent(cur)
		if err != nil {
			return refuse(err, "parent unreadable")
		}
		if parent <= 1 {
			return nil
		}
		cur = parent
	}
	return refuse(cascade.New(cascade.KindInvalidInput, "agents: ancestor chain too deep"), "more than 64 ancestors")
}

// SpawnRegistry is the set of agent spawns running now, the live-spawn source
// AgentPeerGuard reads (pass Live). Whatever starts a spawn registers it with
// its lease marker before the child runs, fills in the pid and process group
// once they exist, and releases it only after the lease sweep returned nil.
// The zero value is not usable; use NewSpawnRegistry. Safe for concurrent use.
type SpawnRegistry struct {
	mu     sync.Mutex
	spawns map[*SpawnEntry]LiveSpawn
}

// SpawnEntry is one registration in a SpawnRegistry.
type SpawnEntry struct{ reg *SpawnRegistry }

// NewSpawnRegistry returns an empty registry.
func NewSpawnRegistry() *SpawnRegistry {
	return &SpawnRegistry{spawns: map[*SpawnEntry]LiveSpawn{}}
}

// Register records s as live and returns its entry.
func (r *SpawnRegistry) Register(s LiveSpawn) *SpawnEntry {
	e := &SpawnEntry{reg: r}
	r.mu.Lock()
	r.spawns[e] = s
	r.mu.Unlock()
	return e
}

// Set replaces the entry's identifiers (for instance once the pid is known).
// It does nothing after Release.
func (e *SpawnEntry) Set(s LiveSpawn) {
	e.reg.mu.Lock()
	defer e.reg.mu.Unlock()
	if _, ok := e.reg.spawns[e]; ok {
		e.reg.spawns[e] = s
	}
}

// Release drops the entry. Call it only after the spawn's lease sweep
// returned nil; releasing twice is harmless.
func (e *SpawnEntry) Release() {
	e.reg.mu.Lock()
	delete(e.reg.spawns, e)
	e.reg.mu.Unlock()
}

// Live returns a copy of the registered spawns.
func (r *SpawnRegistry) Live() []LiveSpawn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LiveSpawn, 0, len(r.spawns))
	for _, s := range r.spawns {
		out = append(out, s)
	}
	return out
}
