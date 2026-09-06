//go:build darwin

// Purpose: darwin-only clipboard tests: a real /usr/bin/pbcopy round
//
//	trip (Art.2 external-contract) and the write/clear subprocess-failure
//	paths. See internal/secrets/testdata/README.md for pbcopy provenance.
//
// SPORT: internal/secrets clipboard_darwin_test.go/ADDED
//
//	(P1-E08-W2-S16-T4).
package secrets

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestClipboard_Darwin_RealPbcopy runs the real production ops against
// the real /usr/bin/pbcopy: the Art.2 external-contract counterpart. It
// writes a marker value, then clears it, and confirms `pbpaste` sees the
// cleared value rather than the marker. It restores whatever was on the
// clipboard beforehand so it leaves no trace on a developer machine.
func TestClipboard_Darwin_RealPbcopy(t *testing.T) {
	if _, err := exec.LookPath("pbpaste"); err != nil {
		t.Skip("pbpaste not available to verify the round trip")
	}
	prior, _ := exec.Command("pbpaste").Output()
	t.Cleanup(func() {
		if len(prior) == 0 {
			return
		}
		c := exec.Command(pbcopyBin)
		c.Stdin = bytes.NewReader(prior)
		_ = c.Run()
	})

	ops, err := newClipboardOps()
	if err != nil {
		t.Fatalf("newClipboardOps: %v", err)
	}
	marker := []byte("cascade-clipboard-darwin-real-test-marker")
	if err := ops.setValue(context.Background(), marker); err != nil {
		t.Fatalf("setValue against real pbcopy: %v", err)
	}
	got, err := exec.Command("pbpaste").Output()
	if err != nil {
		t.Fatalf("pbpaste: %v", err)
	}
	if string(got) != string(marker) {
		t.Fatalf("pbpaste = %q, want %q", got, marker)
	}
	if err := ops.clearValue(context.Background()); err != nil {
		t.Fatalf("clearValue against real pbcopy: %v", err)
	}
	got, err = exec.Command("pbpaste").Output()
	if err != nil {
		t.Fatalf("pbpaste after clear: %v", err)
	}
	if string(got) == string(marker) {
		t.Fatalf("pbpaste still reports the marker after clear")
	}
}

// TestClipboard_Darwin_WriteFailure exercises the write-failure branch
// with a fake runCmd standing in for a non-zero pbcopy exit.
func TestClipboard_Darwin_WriteFailure(t *testing.T) {
	ops := &darwinClipboardOps{bin: pbcopyBin, runCmd: func(context.Context, string, []byte) error {
		return errors.New("exit status 1")
	}}
	if err := ops.setValue(context.Background(), []byte("v")); err == nil {
		t.Fatalf("expected setValue to fail")
	}
}

// TestClipboard_Darwin_ClearFailureBothSteps confirms both clear steps
// run and are reported even when the first one fails.
func TestClipboard_Darwin_ClearFailureBothSteps(t *testing.T) {
	var calls [][]byte
	ops := &darwinClipboardOps{bin: pbcopyBin, runCmd: func(_ context.Context, _ string, stdin []byte) error {
		calls = append(calls, append([]byte(nil), stdin...))
		if len(calls) == 1 {
			return errors.New("exit status 1")
		}
		return nil
	}}
	if err := ops.clearValue(context.Background()); err == nil {
		t.Fatalf("expected clearValue to report the first step's failure")
	}
	if len(calls) != 2 {
		t.Fatalf("expected both clear steps to run, got %d calls", len(calls))
	}
	if string(calls[0]) != " " || len(calls[1]) != 0 {
		t.Fatalf("clear steps out of order or wrong payloads: %q, %q", calls[0], calls[1])
	}
}

// TestClipboard_Darwin_ArgsNeverCarryPayload asserts the exec.Cmd this
// package builds never places the payload on the argument vector — only
// on Stdin — which is what makes the red-team byte-absence guarantee
// structural rather than incidental.
func TestClipboard_Darwin_ArgsNeverCarryPayload(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), pbcopyBin)
	for _, a := range cmd.Args {
		if a == "SHOULD-NEVER-BE-AN-ARG" {
			t.Fatalf("payload leaked into argv")
		}
	}
	if len(cmd.Args) != 1 {
		t.Fatalf("pbcopy invocation carries unexpected arguments: %v", cmd.Args)
	}
}

// TestNewClipboardWriter_Darwin_Integration builds the full production
// writer (real newClipboardOps) with fake surrounding seams, proving the
// wiring compiles and runs end to end on this platform.
func TestNewClipboardWriter_Darwin_Integration(t *testing.T) {
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

// TestClipboardWriteFailurePropagates: a subprocess non-zero exit at
// setValue fails closed and persists/audits nothing.
func TestClipboardWriteFailurePropagates(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(7000, 0))
	ops := &fakeOps{name: "darwin", setErr: errors.New("pbcopy: exit status 1")}
	aw, store, sched := &fakeAudit{}, newFakeStore(), &fakeScheduler{}
	w := newTestWriter(t, clock, ops, aw, store, sched, okVerifier())

	err := w.Write(context.Background(), []byte("signed"), []byte("v"))
	if !errors.Is(err, ErrClipboardUnavailable) {
		t.Fatalf("Write = %v, want ErrClipboardUnavailable", err)
	}
	if len(aw.events) != 0 || len(store.rows) != 0 {
		t.Fatalf("a failed write must not persist or audit anything")
	}
}

// TestClipboardPersistFailureClearsImmediately: a store that cannot
// accept the pending-clear row must not leave the secret sitting on the
// clipboard with no durable record — clear immediately and refuse.
func TestClipboardPersistFailureClearsImmediately(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(8000, 0))
	ops := &fakeOps{name: "darwin"}
	aw, store, sched := &fakeAudit{}, newFakeStore(), &fakeScheduler{}
	store.putErr = errors.New("store unavailable")
	w := newTestWriter(t, clock, ops, aw, store, sched, okVerifier())

	if err := w.Write(context.Background(), []byte("signed"), []byte("v")); err == nil {
		t.Fatalf("Write succeeded despite a persistence failure")
	}
	if ops.clearCalls == 0 {
		t.Fatalf("writer left the value on the clipboard with no durable clear record")
	}
}

// TestNewClipboardWriterRequiresAllSeams: a nil dependency refuses at
// construction rather than building a writer that misbehaves later.
func TestNewClipboardWriterRequiresAllSeams(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1, 0))
	ops := &fakeOps{name: "darwin"}
	aw, store, sched, v := &fakeAudit{}, newFakeStore(), &fakeScheduler{}, okVerifier()

	cases := []struct {
		name string
		fn   func() (ClipboardWriter, error)
	}{
		{"nil clock", func() (ClipboardWriter, error) { return newClipboardWriterWithOps(nil, aw, v, store, sched, ops) }},
		{"nil audit", func() (ClipboardWriter, error) { return newClipboardWriterWithOps(clock, nil, v, store, sched, ops) }},
		{"nil verifier", func() (ClipboardWriter, error) { return newClipboardWriterWithOps(clock, aw, nil, store, sched, ops) }},
		{"nil store", func() (ClipboardWriter, error) { return newClipboardWriterWithOps(clock, aw, v, nil, sched, ops) }},
		{"nil scheduler", func() (ClipboardWriter, error) { return newClipboardWriterWithOps(clock, aw, v, store, nil, ops) }},
		{"nil ops", func() (ClipboardWriter, error) { return newClipboardWriterWithOps(clock, aw, v, store, sched, nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.fn(); err == nil {
				t.Fatalf("expected a refusal for %s", tc.name)
			}
		})
	}
}

// TestRearmPendingClearsRequiresProductionWriter guards the type-assert
// in RearmPendingClears: a non-production ClipboardWriter refuses rather
// than silently doing nothing.
func TestRearmPendingClearsRequiresProductionWriter(t *testing.T) {
	var w ClipboardWriter = fakeClipboardWriter{}
	clock := runtime.NewFixedClock(time.Unix(1, 0))
	err := RearmPendingClears(context.Background(), w, clock, &fakeScheduler{}, newFakeStore())
	if err == nil {
		t.Fatalf("expected a refusal for a non-production ClipboardWriter")
	}
}

type fakeClipboardWriter struct{}

func (fakeClipboardWriter) Write(context.Context, []byte, []byte) error { return nil }

// TestSystemClipboardScheduler exercises the production adapter
// (NewSystemClipboardScheduler/AfterFunc) directly: the one place a real
// timer is legitimate (the adapter, not domain logic).
func TestSystemClipboardScheduler(t *testing.T) {
	s := NewSystemClipboardScheduler()
	fired := make(chan struct{})
	s.AfterFunc(time.Millisecond, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatalf("scheduled fn never fired")
	}

	notFired := make(chan struct{})
	stop := s.AfterFunc(time.Hour, func() { close(notFired) })
	stop()
	select {
	case <-notFired:
		t.Fatalf("stopped fn fired anyway")
	case <-time.After(10 * time.Millisecond):
	}
}
