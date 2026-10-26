//go:build !windows && integration

package agents

// Purpose: the owner guard against real processes and a real socket. A real
// rpc.Handler serves a unix socket under a temp dir; the test-only reapchild
// (P1-AGT-13) is spawned with a live lease marker and no principal token, dials
// with the owner-CLI marker, and must get HTTP 403 KindPermissionDenied. The
// test process itself, outside every live tree, keeps the owner view, and the
// peer pid the guard sees is the dialing process's real pid.
// Constraints: spawned children are only testdata/reapchild; HOME and USERPROFILE
//   point at a fresh temp dir (childEnv); the socket lives in a short OS temp dir
//   because a unix socket path cannot exceed ~104 bytes on darwin. The guard is
//   process-wide (rpc.SetOwnerPeerGuard panics on a second call), so one spy guard
//   is installed once and each test sets the live list (fixture file).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAgentChildDialsAsOwnerRefused(t *testing.T) {
	bin := reapChildBin(t)
	srv := startOwnerServer(t)
	for name, reg := range map[string]func(string) func(int) LiveSpawn{
		"whole spawn": leaderLive,
		"marker only": func(m string) func(int) LiveSpawn { return func(int) LiveSpawn { return LiveSpawn{Marker: m} } },
		"group only": func(string) func(int) LiveSpawn {
			return func(pid int) LiveSpawn { return LiveSpawn{PGID: pid} }
		},
		"leader pid only": func(string) func(int) LiveSpawn {
			return func(pid int) LiveSpawn { return LiveSpawn{PID: pid} }
		},
	} {
		useLive(t)
		marker, err := NewLeaseMarker()
		if err != nil {
			t.Fatal(err)
		}
		before := srv.count(ownerVerb)
		got := dialChild(t, bin, "dial", srv.sock, "owner", ownerVerb, marker, reg(marker), addLive, inOwnGroup)
		wantRefused(t, name, got)
		if srv.count(ownerVerb) != before {
			t.Errorf("%s: the refused request reached the handler", name)
		}
	}
}

func TestAgentGrandchildAfterSetsidRefused(t *testing.T) {
	bin := reapChildBin(t)
	srv := startOwnerServer(t)
	useLive(t)
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	// Only the spawn leader is registered, by pid and group. The grandchild is in a new
	// session and group, its environment no longer holds the marker, so only its
	// ppid chain (grandchild -> leader) ties it to the spawn.
	got := dialChild(t, bin, "setsid-dial", srv.sock, "owner", ownerVerb, marker, leaderLive(marker), addLive, inOwnGroup)
	wantRefused(t, "setsid grandchild with a scrubbed environment", got)
	if srv.count(ownerVerb) != 0 {
		t.Error("the refused grandchild reached the handler")
	}
}

func TestOwnerCLIFromNonAgentAllowed(t *testing.T) {
	bin := reapChildBin(t)
	srv := startOwnerServer(t)
	useLive(t)
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	// A live spawn exists (its own process group and session, so the guard has a
	// real group set to match), but neither this process nor a sibling child belongs to it.
	live := exec.Command(bin, "sleep", filepath.Join(t.TempDir(), "pid"))
	live.Env = childEnv(t, marker)
	live.SysProcAttr = inOwnSession
	if err := live.Start(); err != nil {
		t.Fatalf("start the live spawn: %v", err)
	}
	t.Cleanup(func() { _ = live.Process.Kill(); _ = live.Wait() })
	addLive(LiveSpawn{Marker: marker, PID: live.Process.Pid, PGID: live.Process.Pid})

	got := selfDial(t, srv.sock, ownerVerb, principal.HeaderOwnerCLI, "owner-cli")
	if got.status != 200 || srv.count(ownerVerb) != 1 {
		t.Fatalf("the test process outside every live tree: status %d served %d body %q, want the owner view", got.status, srv.count(ownerVerb), got.body)
	}
	if pids := seenPIDs(); len(pids) != 1 || pids[0] != os.Getpid() {
		t.Errorf("the guard saw peer pids %v, want [%d] (the dialing process, from the kernel)", pids, os.Getpid())
	}

	sibling := dialChild(t, bin, "dial", srv.sock, "owner", ownerVerb, "", nil, nil, inOwnGroup)
	if sibling.status != 200 || srv.count(ownerVerb) != 2 {
		t.Errorf("a sibling child outside every live tree: status %d served %d body %q, want 200 and served", sibling.status, srv.count(ownerVerb), sibling.body)
	}
	// The production shape: the daemon runs under a service manager and the owner's
	// CLI runs in a terminal session of its own.
	terminal := dialChild(t, bin, "dial", srv.sock, "owner", ownerVerb, "", nil, nil, inOwnSession)
	if terminal.status != 200 || srv.count(ownerVerb) != 3 {
		t.Errorf("a child in its own session outside every live tree: status %d served %d body %q, want 200 and served", terminal.status, srv.count(ownerVerb), terminal.body)
	}
}

func TestOwnerPeerGuardFailsClosedOnUnknownPID(t *testing.T) {
	bin := reapChildBin(t)
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	live := func() []LiveSpawn { return []LiveSpawn{{Marker: marker, PGID: 4242, PID: 4343}} }
	guard := AgentPeerGuard(live)

	gone := startChild(t, bin, "", "sleep", filepath.Join(t.TempDir(), "pid"))
	pid := gone.cmd.Process.Pid
	_ = gone.cmd.Process.Kill()
	<-gone.done // reaped: the pid names no process
	for name, p := range map[string]int{"a peer that exited before the check": pid, "pid zero": 0, "init": 1, "negative": -7} {
		err := guard(context.Background(), p)
		if k, ok := cascade.KindOf(err); err == nil || !ok || k != cascade.KindPermissionDenied ||
			!strings.Contains(err.Error(), "agents: the socket peer could not be cleared") {
			t.Errorf("%s (pid %d): err %v, want the guard's could-not-be-cleared refusal", name, p, err)
		}
	}
}

func TestPrincipalRequestsUnaffectedByGuard(t *testing.T) {
	bin := reapChildBin(t)
	srv := startOwnerServer(t)
	useLive(t)
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	// A live agent child carrying a principal token is not judged by pid.
	got := dialChild(t, bin, "dial", srv.sock, "token:"+srv.token, scopedVerb, marker, leaderLive(marker), addLive, inOwnGroup)
	if got.status != 200 || srv.count(scopedVerb) != 1 {
		t.Errorf("principal request from a live agent child: status %d served %d body %q, want 200 and served", got.status, srv.count(scopedVerb), got.body)
	}
	if pids := seenPIDs(); len(pids) != 0 {
		t.Errorf("the guard judged a principal request (pids %v)", pids)
	}
	// Both headers: refused as conflicting by the caller admission, not by pid.
	both := dialChild(t, bin, "dial", srv.sock, "both:"+srv.token, scopedVerb, marker, leaderLive(marker), addLive, inOwnGroup)
	if both.status != 400 || srv.count(scopedVerb) != 1 {
		t.Errorf("request with both headers: status %d served %d body %q, want 400 and not served", both.status, srv.count(scopedVerb), both.body)
	}
	if pids := seenPIDs(); len(pids) != 0 {
		t.Errorf("the guard judged a both-headers request (pids %v)", pids)
	}
}
