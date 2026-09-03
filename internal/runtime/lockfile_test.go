// Purpose: unit + fuzz coverage for lockfile.go's pidfile parsing and
//   removal helpers, and lockfile_unix.go's ProcessAlive liveness probe.
// Constraints: Art.7.1 — every filesystem write goes under t.TempDir();
//   FuzzParsePidfile touches no filesystem at all (its target is a pure
//   []byte -> (int, error) function).

package runtime

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParsePidfile_Table(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"plain", "1234", 1234, false},
		{"trailing_newline", "1234\n", 1234, false},
		{"surrounding_whitespace", "  1234  \n", 1234, false},
		{"empty", "", 0, true},
		{"whitespace_only", "   \n", 0, true},
		{"non_integer", "not-a-pid", 0, true},
		{"zero", "0", 0, true},
		{"negative", "-1", 0, true},
		{"float", "12.5", 0, true},
		{"trailing_garbage", "1234x", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePidfile([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePidfile(%q) = %d, nil; want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePidfile(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParsePidfile(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// FuzzParsePidfile proves ParsePidfile never panics on any input,
// including adversarial or non-UTF8 bytes, and that every value it DOES
// accept is a positive integer (the caller-facing invariant lockfile.go
// documents). Seeded from internal/testdata/fuzz/parsepidfile/seed1 per
// this ticket's files_scope.
func FuzzParsePidfile(f *testing.F) {
	for _, seed := range readParsePidfileSeeds(f) {
		f.Add(seed)
	}
	f.Add("1234")
	f.Add("")
	f.Add("0")
	f.Add("-1")
	f.Add("not-a-number")
	f.Add(string([]byte{0xff, 0xfe, 0x00}))

	f.Fuzz(func(t *testing.T, raw string) {
		pid, err := ParsePidfile([]byte(raw))
		if err != nil {
			return // rejection is a valid outcome; only a panic is a bug
		}
		if pid <= 0 {
			t.Fatalf("ParsePidfile(%q) returned non-positive pid %d with nil error", raw, pid)
		}
	})
}

// readParsePidfileSeeds loads internal/testdata/fuzz/parsepidfile/seed1,
// one seed value per line, mirroring fuzz_test.go's readFuzzSeedLines
// helper for the sibling config_literal corpus.
func readParsePidfileSeeds(f *testing.F) []string {
	f.Helper()
	path := filepath.Join("..", "..", "internal", "testdata", "fuzz", "parsepidfile", "seed1")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		lines = append(lines, string(line))
	}
	return lines
}

func TestReadPidfile_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadPidfile(filepath.Join(dir, "daemon.pid"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadPidfile on missing file: got %v, want errors.Is(err, os.ErrNotExist)", err)
	}
}

func TestReadPidfile_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")
	if err := os.WriteFile(path, []byte("4242\n"), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}
	pid, err := ReadPidfile(path)
	if err != nil {
		t.Fatalf("ReadPidfile: %v", err)
	}
	if pid != 4242 {
		t.Fatalf("ReadPidfile = %d, want 4242", pid)
	}
}

func TestReadPidfile_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")
	if err := os.WriteFile(path, []byte("not-an-integer"), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}
	if _, err := ReadPidfile(path); err == nil {
		t.Fatal("ReadPidfile on unparsable content: want error, got nil")
	}
}

func TestRemovePidfile_IdempotentOnMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")
	if err := RemovePidfile(path); err != nil {
		t.Fatalf("RemovePidfile on already-absent file: %v", err)
	}
}

func TestRemovePidfile_RemovesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")
	if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}
	if err := RemovePidfile(path); err != nil {
		t.Fatalf("RemovePidfile: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pidfile still present after RemovePidfile: %v", err)
	}
	// Idempotent: a second call on the now-clean state is still a no-op.
	if err := RemovePidfile(path); err != nil {
		t.Fatalf("second RemovePidfile call: %v", err)
	}
}

func TestRemoveSocketFile_RemovesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.sock")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed socket file: %v", err)
	}
	if err := RemoveSocketFile(path); err != nil {
		t.Fatalf("RemoveSocketFile: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file still present after RemoveSocketFile: %v", err)
	}
}

func TestProcessAlive_SelfIsAlive(t *testing.T) {
	liveness, err := ProcessAlive(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessAlive(self): unexpected error: %v", err)
	}
	if liveness != ProcessLivenessAlive {
		t.Fatalf("ProcessAlive(self) = %v, want ProcessLivenessAlive", liveness)
	}
}

func TestProcessAlive_ExitedProcessIsDead(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn+run short-lived child: %v", err)
	}
	pid := cmd.Process.Pid

	liveness, err := ProcessAlive(pid)
	if err != nil {
		t.Fatalf("ProcessAlive(exited pid %d): unexpected error: %v", pid, err)
	}
	if liveness != ProcessLivenessDead {
		t.Fatalf("ProcessAlive(exited pid %d) = %v, want ProcessLivenessDead", pid, liveness)
	}
}

func TestProcessAlive_InvalidPidIsUndecided(t *testing.T) {
	liveness, err := ProcessAlive(0)
	if err == nil {
		t.Fatal("ProcessAlive(0): want error, got nil")
	}
	if liveness != ProcessLivenessUndecided {
		t.Fatalf("ProcessAlive(0) = %v, want ProcessLivenessUndecided", liveness)
	}
}
