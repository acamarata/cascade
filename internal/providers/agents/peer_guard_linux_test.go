//go:build linux

package agents

// Purpose: the linux peer inspector against a fixture procfs tree and against
// this process, and the dumpable flag HideDaemonEnviron sets.
// Constraints: fixtures live under t.TempDir(); no socket and no "net" import
//   (this file is in the unit lane). The flag test re-runs this binary as a
//   separate process so the test runner is never made non-dumpable.

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseStat(t *testing.T) {
	for name, tc := range map[string]struct {
		line            string
		ppid, pgid, sid int
		wantErr         bool
	}{
		"plain":              {"123 (bash) S 100 123 100 34816 123 4194560 1 0", 100, 123, 100, false},
		"name with spaces":   {"42 (my daemon) S 7 8 9 0 -1", 7, 8, 9, false},
		"name with brackets": {"42 (a) b) (c)) R 11 12 13 0", 11, 12, 13, false},
		"no command":         {"42 S 7 8 9", 0, 0, 0, true},
		"too few fields":     {"42 (x) S 7 8", 0, 0, 0, true},
		"non numeric":        {"42 (x) S seven 8 9 0", 0, 0, 0, true},
	} {
		ppid, pgid, sid, err := parseStat([]byte(tc.line))
		if tc.wantErr {
			if err != errStatShape {
				t.Errorf("%s: err %v, want the errStatShape sentinel", name, err)
			}
			continue
		}
		if err != nil || ppid != tc.ppid || pgid != tc.pgid || sid != tc.sid {
			t.Errorf("%s: got %d %d %d err %v, want %d %d %d", name, ppid, pgid, sid, err, tc.ppid, tc.pgid, tc.sid)
		}
	}
	if errStatShape.Error() != "agents: unexpected /proc stat format" {
		t.Errorf("errStatShape message %q", errStatShape.Error())
	}
}

// fixtureProc writes root/<pid>/{environ,stat}.
func fixtureProc(t *testing.T, root string, pid int, environ, stat string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(environ), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProcInspectorFixture(t *testing.T) {
	root := t.TempDir()
	fixtureProc(t, root, 10, "HOME=/h\x00CASCADE_LEASE_ID="+markerA+"\x00", "10 (cat) S 4 5 6 0")
	fixtureProc(t, root, 11, "HOME=/h", "11 (x) S 1 11 11 0")
	insp := procInspector{root: root}

	if held, err := insp.holds(10, []string{"CASCADE_LEASE_ID=" + markerB, "CASCADE_LEASE_ID=" + markerA}); err != nil || !held {
		t.Errorf("holds the live marker: %v %v, want true", held, err)
	}
	if held, err := insp.holds(11, []string{"CASCADE_LEASE_ID=" + markerA}); err != nil || held {
		t.Errorf("holds without the marker: %v %v, want false", held, err)
	}
	if ppid, err := insp.parent(10); err != nil || ppid != 4 {
		t.Errorf("parent(10) = %d, %v, want 4", ppid, err)
	}
	if pgid, sid, err := insp.groups(10); err != nil || pgid != 5 || sid != 6 {
		t.Errorf("groups(10) = %d %d, %v, want 5 6", pgid, sid, err)
	}

	// A process that is gone is an error, never "no entries".
	if _, err := insp.holds(99, []string{"A=b"}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("holds of a missing pid: %v, want ErrNotExist", err)
	}
	if _, err := insp.parent(99); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("parent of a missing pid: %v, want ErrNotExist", err)
	}
	if _, _, err := insp.groups(99); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("groups of a missing pid: %v, want ErrNotExist", err)
	}

	// A bad stat line is the shape error.
	fixtureProc(t, root, 12, "", "garbage")
	if _, err := insp.parent(12); err != errStatShape {
		t.Errorf("parent of a garbage stat: %v, want errStatShape", err)
	}
}

func TestProcInspectorRefusesUnreadableAndOversizedEnviron(t *testing.T) {
	root := t.TempDir()
	insp := procInspector{root: root}
	// An unreadable environ (a non-dumpable peer) is an error: the guard refuses it.
	if os.Geteuid() != 0 {
		fixtureProc(t, root, 13, "A=b", "13 (x) S 1 13 13 0")
		if err := os.Chmod(filepath.Join(root, "13", "environ"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := insp.holds(13, []string{"A=b"}); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("holds of an unreadable environ: %v, want ErrPermission", err)
		}
	}

	// An environ over the read limit is an error, not a truncated read.
	fixtureProc(t, root, 14, strings.Repeat("A=b\x00", maxEnvironBytes/4+8), "14 (x) S 1 14 14 0")
	if _, err := insp.holds(14, []string{"Z=z"}); err == nil || err.Error() != "invalid-input: agents: environ larger than the read limit" {
		t.Errorf("holds of an oversized environ: %v, want the read-limit error", err)
	}
}

func TestProcInspectorOnThisProcess(t *testing.T) {
	insp, err := platformPeerInspector()
	if err != nil {
		t.Fatal(err)
	}
	if ppid, err := insp.parent(os.Getpid()); err != nil || ppid != os.Getppid() {
		t.Errorf("parent(self) = %d, %v, want %d", ppid, err, os.Getppid())
	}
	if pgid, sid, err := insp.groups(os.Getpid()); err != nil || pgid != syscall.Getpgrp() {
		t.Errorf("groups(self) = %d %d, %v, want pgid %d", pgid, sid, err, syscall.Getpgrp())
	}
	if held, err := insp.holds(os.Getpid(), []string{os.Environ()[0]}); err != nil || !held {
		t.Errorf("holds(self, first environ entry) = %v, %v, want true", held, err)
	}
	if held, err := insp.holds(os.Getpid(), []string{"CASCADE_NOT_SET_ANYWHERE=1"}); err != nil || held {
		t.Errorf("holds(self, absent entry) = %v, %v, want false", held, err)
	}
}

func TestHideDaemonEnvironClearsTheDumpableFlag(t *testing.T) {
	if os.Getenv("CASCADE_TEST_DUMPABLE_HELPER") == "1" {
		before, _ := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
		if err := HideDaemonEnviron(); err != nil {
			_, _ = os.Stdout.WriteString("error " + err.Error())
			return
		}
		after, _ := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
		_, _ = os.Stdout.WriteString("before=" + strconv.Itoa(before) + " after=" + strconv.Itoa(after))
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHideDaemonEnvironClearsTheDumpableFlag$")
	cmd.Env = append(os.Environ(), "CASCADE_TEST_DUMPABLE_HELPER=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v", err)
	}
	if !strings.Contains(string(out), "before=1 after=0") {
		t.Errorf("helper reported %q, want before=1 after=0", out)
	}
}

func TestProcInspectorRefusesAnExitedPeer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "42")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	insp := procInspector{root: root}
	for _, st := range []string{"Z", "X"} {
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte("42 (a b) "+st+" 1 42 42 0"), 0o600); err != nil {
			t.Fatal(err)
		}
		if held, err := insp.holds(42, []string{"CASCADE_LEASE_ID=x"}); held || err != errPeerExited {
			t.Errorf("state %s: held %v err %v, want errPeerExited", st, held, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte("42 (a b) S 1 42 42 0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if held, err := insp.holds(42, []string{"CASCADE_LEASE_ID=x"}); held || err != nil {
		t.Errorf("live peer with an empty environ: held %v err %v, want false nil", held, err)
	}
}
