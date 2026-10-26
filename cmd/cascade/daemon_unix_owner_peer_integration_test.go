//go:build !windows && integration

package main

// Purpose: prove the daemon composition itself arms the owner-peer seam
//   (P1-AGT-16): buildRPCServer's real server, served on a real unix socket,
//   refuses an owner-CLI request from a process carrying a live spawn's lease
//   marker, admits the same request from a process outside every live spawn,
//   and (linux) leaves the daemon's environment unreadable to a same-uid
//   sibling. The children are the P1-AGT-13 reapchild binary.
// Constraints: everything is written under t.TempDir(); the spawn registered
//   in daemonSpawns is released at cleanup.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/providers/agents"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// ownerDial runs reapchild's dial mode as an owner-CLI call of status.get with
// env, and returns the HTTP status and body it recorded.
func ownerDial(t *testing.T, bin, sock string, env []string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	out, gofile := filepath.Join(dir, "out"), filepath.Join(dir, "go")
	if err := os.WriteFile(gofile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "dial", sock, out, "owner", daemon.StatusMethod, gofile)
	cmd.Env = env
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reapchild dial: %v %s", err, msg)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := strings.Cut(string(raw), "\n")
	n, err := strconv.Atoi(code)
	if err != nil {
		t.Fatalf("reapchild recorded %q", raw)
	}
	return n, body
}

// buildReapChild builds the agents package's reapchild into a temp dir.
func buildReapChild(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "reapchild")
	if out, err := exec.Command("go", "build", "-o", bin, "../../internal/providers/agents/testdata/reapchild").CombinedOutput(); err != nil {
		t.Fatalf("go build reapchild: %v\n%s", err, out)
	}
	return bin
}

// withoutLeaseEnv returns this process's environment minus any lease marker
// or principal token.
func withoutLeaseEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, agents.LeaseMarkerEnv+"=") && !strings.HasPrefix(e, "CASCADE_PRINCIPAL_TOKEN=") {
			env = append(env, e)
		}
	}
	return env
}

func TestDaemonSocketRefusesAgentOwnerCLI(t *testing.T) {
	bin := buildReapChild(t)
	clock := cruntime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	sock := filepath.Join(t.TempDir(), "d.sock")
	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: sock}, fakeMemoryPaths{root: t.TempDir()}, nil, storetest.NewMemStore())
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	serveOnSocketIT(t, srv, sock)

	marker, err := agents.NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	entry := daemonSpawns.Register(agents.LiveSpawn{Marker: marker})
	t.Cleanup(entry.Release)

	clean := withoutLeaseEnv()
	if code, body := ownerDial(t, bin, sock, append(clean, agents.LeaseMarkerEnv+"="+marker)); code != 403 ||
		!strings.Contains(body, `"message":"agent processes may not act as the owner"`) {
		t.Errorf("agent child as owner: status %d body %q, want 403 agent processes may not act as the owner", code, body)
	}
	if code, body := ownerDial(t, bin, sock, clean); code != 200 || !strings.Contains(body, `"result"`) {
		t.Errorf("non-agent child as owner: status %d body %q, want 200 with a result", code, body)
	}
	if runtime.GOOS == "linux" {
		out, err := exec.Command("cat", "/proc/"+strconv.Itoa(os.Getpid())+"/environ").CombinedOutput()
		if err == nil || !strings.Contains(string(out), "Permission denied") {
			t.Errorf("a same-uid sibling read the daemon environ: err %v out %.80q, want Permission denied", err, out)
		}
	}
}
