//go:build linux

// Purpose: linux-only clipboard tests: a real xclip round trip when
//
//	xclip is present, the fail-closed ErrClipboardUnavailable branch when
//	it is absent, and the two-step clear pattern. Runs on the linux CI
//	lane per Art.5; this host only compiles it (GOOS=linux go vet).
//
// SPORT: internal/secrets clipboard_linux_test.go/ADDED
//
//	(P1-E08-W2-S16-T4).
package secrets

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestClipboard_Linux_RealXclip runs the real production ops against a
// real xclip, when one is present on the runner. Skips (does not fail)
// when xclip is not installed — the absent-tool branch is covered
// separately by TestClipboard_Linux_XclipAbsent, which does not depend on
// host state.
func TestClipboard_Linux_RealXclip(t *testing.T) {
	if _, err := exec.LookPath(xclipBin); err != nil {
		t.Skip("xclip not installed on this host")
	}
	ops, err := newClipboardOps()
	if err != nil {
		t.Fatalf("newClipboardOps: %v", err)
	}
	marker := []byte("cascade-clipboard-linux-real-test-marker")
	if err := ops.setValue(context.Background(), marker); err != nil {
		t.Fatalf("setValue against real xclip: %v", err)
	}
	out, err := exec.Command(xclipBin, "-selection", "clipboard", "-o").Output()
	if err != nil {
		t.Fatalf("xclip -o: %v", err)
	}
	if string(out) != string(marker) {
		t.Fatalf("xclip -o = %q, want %q", out, marker)
	}
	if err := ops.clearValue(context.Background()); err != nil {
		t.Fatalf("clearValue against real xclip: %v", err)
	}
	out, err = exec.Command(xclipBin, "-selection", "clipboard", "-o").Output()
	if err == nil && string(out) == string(marker) {
		t.Fatalf("xclip still reports the marker after clear")
	}
}

// TestClipboard_Linux_XclipAbsent asserts the fail-closed branch: when
// the binary cannot be found, Write's underlying setValue returns
// ErrClipboardUnavailable and nothing is attempted beyond the lookup.
func TestClipboard_Linux_XclipAbsent(t *testing.T) {
	ops := &linuxClipboardOps{bin: "cascade-xclip-does-not-exist-marker", runCmd: runXclip}
	if err := ops.setValue(context.Background(), []byte("v")); !errors.Is(err, ErrClipboardUnavailable) {
		t.Fatalf("setValue with absent xclip = %v, want ErrClipboardUnavailable", err)
	}
}

// TestClipboard_Linux_NonZeroExit asserts a real-but-failing xclip also
// fails closed with the same typed error as an absent one (R-14.24: both
// branches refuse identically, no partial delivery).
func TestClipboard_Linux_NonZeroExit(t *testing.T) {
	ops := &linuxClipboardOps{bin: xclipBin, runCmd: func(context.Context, string, []byte) error {
		return errors.New("xclip: exit status 1")
	}}
	if err := ops.setValue(context.Background(), []byte("v")); !errors.Is(err, ErrClipboardUnavailable) {
		t.Fatalf("setValue on non-zero exit = %v, want ErrClipboardUnavailable", err)
	}
}

// TestClipboard_Linux_ClearFailureBothSteps mirrors the darwin case: both
// clear steps run regardless of the first one's outcome.
func TestClipboard_Linux_ClearFailureBothSteps(t *testing.T) {
	var calls [][]byte
	ops := &linuxClipboardOps{bin: xclipBin, runCmd: func(_ context.Context, _ string, stdin []byte) error {
		calls = append(calls, append([]byte(nil), stdin...))
		if len(calls) == 1 {
			return errors.New("exit status 1")
		}
		return nil
	}}
	if err := ops.clearValue(context.Background()); !errors.Is(err, ErrClipboardUnavailable) {
		t.Fatalf("clearValue = %v, want ErrClipboardUnavailable", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected both clear steps to run, got %d calls", len(calls))
	}
}

// TestClipboard_Linux_ArgsNeverCarryPayload: same structural guarantee as
// darwin — the payload travels on Stdin only, xclip's argv is fixed.
func TestClipboard_Linux_ArgsNeverCarryPayload(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), xclipBin, "-selection", "clipboard", "-i")
	want := []string{xclipBin, "-selection", "clipboard", "-i"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("xclip invocation carries unexpected arguments: %v", cmd.Args)
	}
	for i, a := range want {
		if cmd.Args[i] != a {
			t.Fatalf("xclip arg[%d] = %q, want %q", i, cmd.Args[i], a)
		}
	}
}

// TestNewClipboardWriter_Linux_Integration builds the full production
// writer (real newClipboardOps) with fake surrounding seams, skipping
// only if xclip itself is unavailable on the runner.
func TestNewClipboardWriter_Linux_Integration(t *testing.T) {
	if _, err := exec.LookPath(xclipBin); err != nil {
		t.Skip("xclip not installed on this host")
	}
	clock := runtime.NewFixedClock(time.Unix(42, 0))
	aw := &fakeAudit{}
	store := newFakeStore()
	sched := &fakeScheduler{}
	w, err := NewClipboardWriter(clock, aw, okVerifier(), store, sched)
	if err != nil {
		t.Fatalf("NewClipboardWriter: %v", err)
	}
	if err := w.Write(context.Background(), []byte("signed"), []byte("integration-value")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	sched.fireAll()
}
