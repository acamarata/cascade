package main

// Purpose: the init adapters that reach OUTSIDE this process
//   (P1-E16-W4-S35-T6): the subprocess hand-off, the capturing variant
//   the helper enrollment reads, and the storage probe's path
//   resolution. Split from init_e2e_test.go for Art.10.3's 300-line cap.
// Constraints: the subprocess cases drive a REAL child — os.Executable
//   under `go test` is this test binary, so they pass its own flags and
//   select no tests.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestTheSubprocessAdapterRunsARealChild covers both outcomes of the
// hand-off the worker profile and the provider step depend on.
//
// It runs THIS test binary, which is what os.Executable resolves to under
// `go test` — a real process, really started, really waited on. Selecting
// no tests makes the success case fast and side-effect-free; an
// unparseable flag makes the failure case deterministic.
func TestTheSubprocessAdapterRunsARealChild(t *testing.T) {
	out := &bytes.Buffer{}
	sub := initSubprocess{in: strings.NewReader(""), out: out, err: out}

	if err := sub.Run(context.Background(), "-test.run=^$", "-test.count=1"); err != nil {
		t.Fatalf("a child that exits 0 was reported as a failure: %v\n%s", err, out)
	}

	err := sub.Run(context.Background(), "-test.this-flag-does-not-exist")
	if err == nil {
		t.Fatal("a child that exited non-zero was reported as a success")
	}
	if !strings.Contains(err.Error(), "cascade") {
		t.Errorf("the failure does not name the command that failed: %v", err)
	}
}

// TestRunCascadeCapturesOutput covers the capturing variant the helper
// enrollment reads its fingerprint from, including the failure path that
// must carry the child's own message.
func TestRunCascadeCapturesOutput(t *testing.T) {
	out, err := runCascade(context.Background(), "-test.run=^$", "-test.count=1", "-test.v=true")
	if err != nil {
		t.Fatalf("runCascade: %v\n%s", err, out)
	}
	if !strings.Contains(out, "PASS") && !strings.Contains(out, "ok") && out == "" {
		t.Errorf("runCascade captured nothing: %q", out)
	}

	if _, err = runCascade(context.Background(), "-test.this-flag-does-not-exist"); err == nil {
		t.Fatal("a failing child was reported as a success")
	} else if !strings.Contains(err.Error(), "flag") {
		t.Errorf("the child's own message was lost: %v", err)
	}
}

// TestNearestExistingWalksUpAndRefuses covers the --check probe's path
// resolution, including the case a stat cannot answer.
func TestNearestExistingWalksUpAndRefuses(t *testing.T) {
	root := t.TempDir()

	got, err := nearestExisting(filepath.Join(root, "a", "b", "c"))
	if err != nil {
		t.Fatalf("nearestExisting: %v", err)
	}
	if got != root {
		t.Errorf("nearestExisting walked to %q, want %q", got, root)
	}

	file := filepath.Join(root, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding a file: %v", err)
	}
	if _, err := nearestExisting(file); err == nil {
		t.Error("a path that exists and is not a directory was accepted as the cascade home")
	}
}

// TestTheProbeRefusesAnUnwritableHome is the preflight's whole point: a
// directory that exists but cannot be written to passes every cheaper
// check and then fails at the first step that matters.
func TestTheProbeRefusesAnUnwritableHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		// A POSIX mode bit is not how this platform denies a write, so
		// there is nothing here to exercise. The probe's behaviour is
		// unchanged; only the way to make a directory unwritable is.
		t.Skip("a 0500 directory is not read-only on this platform")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0500 directory is still writable, so this cannot be exercised")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("seeding a read-only directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	if err := (initStorageProbe{}).Probe(context.Background(), locked, true); err == nil {
		t.Fatal("the probe passed on a directory it cannot write to")
	}
}
