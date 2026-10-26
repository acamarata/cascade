package agents

// Purpose: AgentPeerGuard's ancestor walk against the fake process view: a chain
// that reaches a live spawn, one that ends at init, and the 64-hop bound.
// Constraints: see peer_guard_test.go (fake inspector, no process, no socket).

import (
	"context"
	"testing"
)

func TestAgentPeerGuardRefusesAncestorOfLiveSpawn(t *testing.T) {
	live := []LiveSpawn{{PGID: 500, PID: 400}}
	chain := map[int]*fakeProc{
		900: {pgid: 900, sid: 900, ppid: 800}, // peer: setsid'd, no marker
		800: {ppid: 700},
		700: {ppid: 400}, // reaches the live pid
		400: {ppid: 1},
	}
	g, _ := guardFor(live, chain)
	wantInTree(t, "ancestor", g.check(context.Background(), 900))

	g, _ = guardFor(live, map[int]*fakeProc{400: {pgid: 400, sid: 400, ppid: 1}})
	wantInTree(t, "the spawn leader itself", g.check(context.Background(), 400))

	// The same chain, but the peer's ancestry ends at init without meeting a live pid.
	g, _ = guardFor(live, map[int]*fakeProc{900: {pgid: 900, sid: 900, ppid: 800}, 800: {ppid: 700}, 700: {ppid: 1}})
	if err := g.check(context.Background(), 900); err != nil {
		t.Errorf("a chain to init was refused: %v", err)
	}
}

// ancestry builds a chain of n processes: pid 1000 is the peer and each has the
// next pid as its parent; the last one's parent is last.
func ancestry(n, last int) map[int]*fakeProc {
	procs := map[int]*fakeProc{}
	for i := 0; i < n; i++ {
		procs[1000+i] = &fakeProc{pgid: 900, sid: 900, ppid: 1000 + i + 1}
	}
	procs[1000+n-1].ppid = last
	return procs
}

func TestAgentPeerGuardBoundsTheAncestorWalk(t *testing.T) {
	// The contract is 64 hops: the peer and 64 ancestors are compared (65 processes).
	live := []LiveSpawn{{PGID: 500, PID: 400}}
	g, _ := guardFor(live, ancestry(65, 1))
	if err := g.check(context.Background(), 1000); err != nil {
		t.Errorf("a peer with 64 ancestors ending at init was refused: %v", err)
	}
	// One more ancestor is past the bound: refused, and the walk stops at the bound.
	procs := ancestry(66, 1)
	g, _ = guardFor(live, procs)
	wantFailedClosed(t, "65 ancestors", g.check(context.Background(), 1000), nil)
	looked := 0
	for _, p := range procs {
		looked += p.parentCalls
	}
	if looked != 65 {
		t.Errorf("looked up %d parents, want exactly 65 (the peer and its 64 ancestors)", looked)
	}
	// The 64th ancestor is still compared against the live pids; the 65th never is.
	live64 := []LiveSpawn{{PID: 1064}}
	g, _ = guardFor(live64, ancestry(66, 1))
	wantInTree(t, "live pid at the 64th ancestor", g.check(context.Background(), 1000))
	g, _ = guardFor([]LiveSpawn{{PID: 1065}}, ancestry(66, 1))
	wantFailedClosed(t, "live pid beyond the bound", g.check(context.Background(), 1000), nil)

	// A cycle never terminates on its own; the bound refuses it.
	g, _ = guardFor(live, map[int]*fakeProc{1000: {pgid: 900, sid: 900, ppid: 1001}, 1001: {ppid: 1000}})
	wantFailedClosed(t, "cycle", g.check(context.Background(), 1000), nil)
}

func TestAgentPeerGuardRefusesAnAncestorInALiveGroup(t *testing.T) {
	// The leader (500) is gone, group 500 still has a member M; P is M's child in
	// a new session with no marker. M was reparented to init, P was not: only M's
	// group ties P to the spawn.
	live := []LiveSpawn{{PGID: 500, PID: 500}}
	g, _ := guardFor(live, map[int]*fakeProc{
		900: {pgid: 900, sid: 900, ppid: 800},
		800: {pgid: 500, sid: 500, ppid: 1},
	})
	wantInTree(t, "ancestor in a live group", g.check(context.Background(), 900))

	g, _ = guardFor(live, map[int]*fakeProc{
		900: {pgid: 900, sid: 900, ppid: 800},
		800: {pgid: 800, sid: 800, ppid: 1},
	})
	if err := g.check(context.Background(), 900); err != nil {
		t.Errorf("an ancestor in another group was refused: %v", err)
	}
}
