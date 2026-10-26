package agents

// Purpose: AgentPeerGuard's decision logic against a fake process view: every
// refusal rule, every fail-closed path, and what is deliberately not matched.
// The same rules against real processes and a real socket are the integration
// tests (peer_guard_integration_test.go).
// Constraints: no process, no socket, no /proc; the inspector is a map. Errors
//   are asserted by identity and message, never by errors.Is alone (it compares
//   Kind only).

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeProc is one process in the fake table.
type fakeProc struct {
	env         []string
	pgid, sid   int
	ppid        int
	envErr      error
	groupsErr   error
	parentErr   error
	envBlocked  bool
	parentCalls int
}

// fakeInspector is a peerInspector over a pid table; a pid absent from the
// table is "no such process" for every call.
type fakeInspector struct {
	procs       map[int]*fakeProc
	holdCalls   int
	holdEntries int // entries passed to the last holds call
}

var errNoSuchProc = errors.New("fake: no such process")

func (f *fakeInspector) holds(pid int, entries []string) (bool, error) {
	f.holdCalls++
	f.holdEntries = len(entries)
	p, ok := f.procs[pid]
	if !ok {
		return false, errNoSuchProc
	}
	if p.envErr != nil {
		return false, p.envErr
	}
	for _, e := range entries {
		for _, have := range p.env {
			if have == e {
				return true, nil
			}
		}
	}
	return false, nil
}

func (f *fakeInspector) groups(pid int) (int, int, error) {
	p, ok := f.procs[pid]
	if !ok {
		return 0, 0, errNoSuchProc
	}
	return p.pgid, p.sid, p.groupsErr
}

func (f *fakeInspector) parent(pid int) (int, error) {
	p, ok := f.procs[pid]
	if !ok {
		return 0, errNoSuchProc
	}
	p.parentCalls++
	return p.ppid, p.parentErr
}

const (
	markerA = "clm_0123456789abcdef0123456789abcdef"
	markerB = "clm_fedcba9876543210fedcba9876543210"
)

// guardFor builds a guard over live and the fake table.
func guardFor(live []LiveSpawn, procs map[int]*fakeProc) (peerGuard, *fakeInspector) {
	insp := &fakeInspector{procs: procs}
	return peerGuard{live: func() []LiveSpawn { return live }, insp: insp}, insp
}

// wantRefused fails unless err is the in-tree refusal, by identity and text.
func wantInTree(t *testing.T, name string, err error) {
	t.Helper()
	if err != errPeerIsAgent {
		t.Errorf("%s: err %v, want the errPeerIsAgent sentinel", name, err)
		return
	}
	if err.Error() != "permission-denied: agents: the socket peer belongs to a live agent spawn" {
		t.Errorf("%s: message %q", name, err.Error())
	}
}

// wantFailedClosed fails unless err refuses with KindPermissionDenied around
// the injected cause.
func wantFailedClosed(t *testing.T, name string, err, cause error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: admitted, want a fail-closed refusal", name)
		return
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindPermissionDenied {
		t.Errorf("%s: kind %v ok %v, want KindPermissionDenied (%v)", name, k, ok, err)
	}
	if cause != nil && errors.Unwrap(err) != cause {
		t.Errorf("%s: wrapped %v, want %v", name, errors.Unwrap(err), cause)
	}
}

func TestAgentPeerGuardAdmitsOutsideEveryTree(t *testing.T) {
	live := []LiveSpawn{{Marker: markerA, PGID: 500, PID: 400}}
	procs := map[int]*fakeProc{
		900: {env: []string{"HOME=/h", "CASCADE_LEASE_ID=" + markerB}, pgid: 900, sid: 880, ppid: 880}, // stale marker, not live
		880: {pgid: 880, sid: 880, ppid: 1},
	}
	g, _ := guardFor(live, procs)
	if err := g.check(context.Background(), 900); err != nil {
		t.Errorf("a peer outside every live tree was refused: %v", err)
	}
}

func TestAgentPeerGuardAdmitsWithoutInspectingWhenNothingIsLive(t *testing.T) {
	g, insp := guardFor(nil, map[int]*fakeProc{})
	if err := g.check(context.Background(), 900); err != nil || insp.holdCalls != 0 {
		t.Errorf("no spawns: err %v after %d inspections, want an immediate admit", err, insp.holdCalls)
	}
}

func TestAgentPeerGuardRefusesLiveMarker(t *testing.T) {
	live := []LiveSpawn{{Marker: markerB}, {Marker: markerA}}
	g, _ := guardFor(live, map[int]*fakeProc{900: {env: []string{"CASCADE_LEASE_ID=" + markerA}, ppid: 1}})
	wantInTree(t, "live marker", g.check(context.Background(), 900))

	// A substring or longer value is not the marker.
	g, _ = guardFor(live, map[int]*fakeProc{900: {env: []string{"CASCADE_LEASE_ID=" + markerA + "x", "X_CASCADE_LEASE_ID=" + markerA}, ppid: 1}})
	if err := g.check(context.Background(), 900); err != nil {
		t.Errorf("a near-miss entry was refused: %v", err)
	}
}

func TestAgentPeerGuardEmptyMarkerNeverWidensTheMatch(t *testing.T) {
	g, insp := guardFor([]LiveSpawn{{Marker: "", PGID: 500}}, map[int]*fakeProc{900: {env: []string{"CASCADE_LEASE_ID="}, pgid: 900, sid: 900, ppid: 1}})
	if err := g.check(context.Background(), 900); err != nil {
		t.Errorf("an empty marker matched a peer: %v", err)
	}
	if insp.holdEntries != 0 {
		t.Errorf("an unusable marker was passed to the environment match (%d entries)", insp.holdEntries)
	}
}

func TestAgentPeerGuardReadsTheEnvironmentEvenWithoutMarkers(t *testing.T) {
	boom := errors.New("fake: environ denied")
	// Only a pid and a group are known yet: the unreadable-environment rule still holds.
	live := []LiveSpawn{{PID: 400, PGID: 500}}
	g, insp := guardFor(live, map[int]*fakeProc{900: {envErr: boom, pgid: 900, sid: 900, ppid: 1}})
	wantFailedClosed(t, "unreadable environment, no marker registered", g.check(context.Background(), 900), boom)
	if insp.holdCalls != 1 || insp.holdEntries != 0 {
		t.Errorf("environment reads %d with %d entries, want one readability read", insp.holdCalls, insp.holdEntries)
	}
	g, _ = guardFor(live, map[int]*fakeProc{900: {pgid: 900, sid: 900, ppid: 1}})
	if err := g.check(context.Background(), 900); err != nil {
		t.Errorf("a readable peer outside the tree was refused: %v", err)
	}
}

func TestAgentPeerGuardRefusesWhenALiveSpawnCannotBeMatched(t *testing.T) {
	// A spawn is live but carries nothing usable (an unknown marker shape, no pid, no group):
	// the guard cannot tell its tree from anyone else, so it refuses rather than admit.
	live := []LiveSpawn{{Marker: "lease-1"}, {Marker: "", PGID: 0, PID: 0}, {Marker: "clm_short", PGID: 1, PID: 1}}
	g, insp := guardFor(live, map[int]*fakeProc{900: {pgid: 900, sid: 900, ppid: 1}})
	err := g.check(context.Background(), 900)
	wantFailedClosed(t, "unmatchable live spawn", err, nil)
	if err != nil && !strings.Contains(err.Error(), "live spawn cannot be matched") {
		t.Errorf("refused for another reason: %v", err)
	}
	if insp.holdCalls != 0 {
		t.Errorf("an unmatchable live list was inspected (%d reads)", insp.holdCalls)
	}
	// One unmatchable spawn next to a valid one still refuses: the rule does not depend on what else is live.
	g, insp = guardFor(append(live, LiveSpawn{Marker: markerA}), map[int]*fakeProc{900: {env: []string{"CASCADE_LEASE_ID=lease-1"}, pgid: 900, sid: 900, ppid: 1}})
	err = g.check(context.Background(), 900)
	wantFailedClosed(t, "unmatchable next to a valid spawn", err, nil)
	if err != nil && !strings.Contains(err.Error(), "live spawn cannot be matched") {
		t.Errorf("mixed list refused for another reason: %v", err)
	}
	if insp.holdCalls != 0 {
		t.Errorf("a mixed list with an unmatchable spawn was inspected (%d reads)", insp.holdCalls)
	}
}

func TestAgentPeerGuardRefusesLiveProcessGroupAndSession(t *testing.T) {
	live := []LiveSpawn{{PGID: 500, PID: 400}}
	g, _ := guardFor(live, map[int]*fakeProc{900: {pgid: 500, sid: 900, ppid: 1}})
	wantInTree(t, "process group", g.check(context.Background(), 900))
	g, _ = guardFor(live, map[int]*fakeProc{900: {pgid: 900, sid: 500, ppid: 1}})
	wantInTree(t, "session", g.check(context.Background(), 900))
	g, _ = guardFor(live, map[int]*fakeProc{900: {pgid: 501, sid: 501, ppid: 1}})
	if err := g.check(context.Background(), 900); err != nil {
		t.Errorf("a neighbouring group was refused: %v", err)
	}
}

func TestAgentPeerGuardFailsClosed(t *testing.T) {
	live := []LiveSpawn{{Marker: markerA, PGID: 500, PID: 400}}
	boom := errors.New("fake: denied")
	cases := map[string]struct {
		procs map[int]*fakeProc
		cause error
	}{
		"environment unreadable": {map[int]*fakeProc{900: {envErr: boom, ppid: 1}}, boom},
		"peer already gone":      {map[int]*fakeProc{}, errNoSuchProc},
		"groups unreadable":      {map[int]*fakeProc{900: {groupsErr: boom, ppid: 1}}, boom},
		"parent unreadable":      {map[int]*fakeProc{900: {pgid: 900, sid: 900, parentErr: boom}}, boom},
		"ancestor vanished":      {map[int]*fakeProc{900: {pgid: 900, sid: 900, ppid: 800}}, errNoSuchProc},
	}
	for name, tc := range cases {
		g, _ := guardFor(live, tc.procs)
		wantFailedClosed(t, name, g.check(context.Background(), 900), tc.cause)
	}
}

func TestAgentPeerGuardRefusesInvalidPIDAndCanceledContext(t *testing.T) {
	live := []LiveSpawn{{Marker: markerA}}
	g, insp := guardFor(live, map[int]*fakeProc{1: {}, 900: {ppid: 1}})
	for _, pid := range []int{-5, 0, 1} {
		wantFailedClosed(t, "invalid pid", g.check(context.Background(), pid), nil)
	}
	if insp.holdCalls != 0 {
		t.Errorf("an invalid pid was inspected (%d reads)", insp.holdCalls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wantFailedClosed(t, "canceled", g.check(ctx, 900), context.Canceled)
}

func TestAgentPeerGuardWiring(t *testing.T) {
	defer func() {
		if got, _ := recover().(string); got != "agents: AgentPeerGuard: nil live-spawn source" {
			t.Errorf("a nil live source panicked with %q", got)
		}
	}()
	guard := AgentPeerGuard(func() []LiveSpawn { return nil })
	err := guard(context.Background(), 12345)
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		// A real guard over an empty live set admits any peer without touching the OS.
		if err != nil {
			t.Errorf("the platform guard refused with nothing live: %v", err)
		}
	} else {
		// No inspector on this platform: every peer is refused, whatever is live.
		wantFailedClosed(t, "unsupported platform", err, nil)
	}
	_ = AgentPeerGuard(nil)
}

// TestSpawnRegistryTracksRegisterSetRelease: Live reports exactly the
// registered spawns, Set replaces identifiers, a Set after Release does not
// resurrect the entry (a dead tree would stay refused), Release twice is harmless.
func TestSpawnRegistryTracksRegisterSetRelease(t *testing.T) {
	reg := NewSpawnRegistry()
	a := reg.Register(LiveSpawn{Marker: "lease-a"})
	b := reg.Register(LiveSpawn{Marker: "lease-b", PGID: 41})
	a.Set(LiveSpawn{Marker: "lease-a", PGID: 77, PID: 78})
	got := reg.Live()
	seen := map[LiveSpawn]bool{}
	for _, s := range got {
		seen[s] = true
	}
	if len(got) != 2 || !seen[LiveSpawn{Marker: "lease-a", PGID: 77, PID: 78}] || !seen[LiveSpawn{Marker: "lease-b", PGID: 41}] {
		t.Fatalf("Live = %v, want lease-a (pgid 77, pid 78) and lease-b (pgid 41)", got)
	}
	got[0].Marker = "mutated"
	if again := reg.Live(); again[0].Marker == "mutated" || again[1].Marker == "mutated" {
		t.Fatal("Live returned the registry's own storage, not a copy")
	}
	a.Release()
	a.Set(LiveSpawn{Marker: "lease-a", PID: 99})
	a.Release()
	if got := reg.Live(); len(got) != 1 || got[0] != (LiveSpawn{Marker: "lease-b", PGID: 41}) {
		t.Fatalf("after Release and a late Set, Live = %v, want only lease-b", got)
	}
	b.Release()
	if got := reg.Live(); len(got) != 0 {
		t.Fatalf("after releasing both, Live = %v, want empty", got)
	}
}
