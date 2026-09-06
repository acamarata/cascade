//go:build windows

// Purpose: windows-only clipboard tests: Write refuses with
//
//	ErrTier2Unsupported and no OS clipboard operation is attempted.
//
// SPORT: internal/secrets clipboard_windows_test.go/ADDED
//
//	(P1-E08-W2-S16-T4).
package secrets

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestClipboard_Windows_Refuses asserts the full writer, built with the
// real windows ops, refuses every Write with ErrTier2Unsupported.
func TestClipboard_Windows_Refuses(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1, 0))
	aw := &fakeAudit{}
	store := newFakeStore()
	sched := &fakeScheduler{}
	w, err := NewClipboardWriter(clock, aw, okVerifier(), store, sched)
	if err != nil {
		t.Fatalf("NewClipboardWriter: %v", err)
	}
	err = w.Write(context.Background(), []byte("signed"), []byte("v"))
	if !errors.Is(err, ErrTier2Unsupported) {
		t.Fatalf("Write = %v, want ErrTier2Unsupported", err)
	}
	if len(aw.events) != 0 || len(store.rows) != 0 {
		t.Fatalf("a refused windows write must not persist or audit anything")
	}
}

// TestClipboard_Windows_OpsRefuseDirectly covers the ops methods
// themselves, independent of the shared Write path.
func TestClipboard_Windows_OpsRefuseDirectly(t *testing.T) {
	ops, err := newClipboardOps()
	if err != nil {
		t.Fatalf("newClipboardOps: %v", err)
	}
	if ops.platform() != "windows" {
		t.Fatalf("platform() = %q, want windows", ops.platform())
	}
	if err := ops.setValue(context.Background(), []byte("v")); !errors.Is(err, ErrTier2Unsupported) {
		t.Fatalf("setValue = %v, want ErrTier2Unsupported", err)
	}
	if err := ops.clearValue(context.Background()); !errors.Is(err, ErrTier2Unsupported) {
		t.Fatalf("clearValue = %v, want ErrTier2Unsupported", err)
	}
}
