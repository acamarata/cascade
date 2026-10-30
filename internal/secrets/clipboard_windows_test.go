//go:build windows

// Purpose: the Windows tier-2 refusal: probe, set and clear all refuse with
//
//	KindUnsupported, and a writer refuses before any record or row.
//
// SPORT: internal/secrets clipboard_windows.go/TEST (P1-SEC-09).
package secrets

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestClipboard_Windows_Refuses(t *testing.T) {
	r := newRig()
	w, err := NewClipboardWriter(runtime.NewFixedClock(clipNow), r.aw, r.fx, fakeVerifier{verb: ClipboardApprovalVerb}, r.store, r.sched, 30*time.Second)
	if err != nil {
		t.Fatalf("NewClipboardWriter: %v", err)
	}
	if err := w.Write(context.Background(), []byte("tok"), []byte("v")); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("Write = %v, want KindUnsupported", err)
	}
	if len(r.tr.list()) != 0 || r.store.len() != 0 {
		t.Fatalf("trace = %v; a refused windows write records nothing", r.tr.list())
	}
}

func TestClipboard_Windows_OpsRefuseDirectly(t *testing.T) {
	ops, _ := newClipboardOps()
	if ops.platform() != "windows" || !cascade.HasKind(ops.probe(), cascade.KindUnsupported) ||
		!cascade.HasKind(ops.setValue(context.Background(), []byte("v")), cascade.KindUnsupported) ||
		!cascade.HasKind(ops.clearValue(context.Background()), cascade.KindUnsupported) {
		t.Fatal("windows ops must refuse every call with KindUnsupported")
	}
}
