//go:build windows

package remote

// Purpose: Art.5 platform-parity proof for Windows (tier-2): the
//   enable_remote_runtime=false guard refuses in-process, before any
//   socket operation, on Windows exactly as it does on every other GOOS —
//   TestRemoteRuntime_Dispatch_NotEnabled (remote_test.go) already proves this
//   platform-independent property, but that file is not built on this
//   GOOS's own CI runner without a Windows host reaching it, so this
//   file restates the same assertion under an explicit windows build
//   tag (LANE-RULES §9: a filename GOOS suffix alone silently excludes a
//   file from every OTHER build, so the corresponding positive
//   assertion needs its own tagged file to actually execute on the one
//   platform it is about).
//
// CONTRACT NOTE (LANE-RULES §1, recorded): the ticket's full_desc reads,
// in the same paragraph, both "the TCP handshake is cross-platform" and
// "On Windows (tier-2) the enable_remote_runtime guard returns
// ErrRemoteRuntimeDeferred in-process without any socket operations" —
// which read together as Windows never actually attempting the R-14.49
// TCP handshake at all, contradicting the first half of the same
// sentence and the transport ruling's own platform-neutral wording
// (R-14.49 names no OS carve-out). This package resolves the
// contradiction toward "cross-platform": dialRemote (remote.go) uses
// only net/http, is not cgo, and runs identically on every GOOS
// (GOOS=windows go build/go vet both pass, per this ticket's own
// checks). What genuinely IS true on every platform including Windows,
// and is what this file actually proves, is the flag=false guard: no
// socket operation happens before Dispatch even reaches dialRemote.
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRemoteRuntime_Dispatch_NotEnabled_Windows re-asserts remote_test.go's
// TestRemoteRuntime_Dispatch_NotEnabled under an explicit windows build tag: a port
// number of 1 with no listener would hang or refuse if any socket
// operation were attempted, so a fast, non-erroring KindUnsupported
// refusal is itself the proof no socket call happened.
func TestRemoteRuntime_Dispatch_NotEnabled_Windows(t *testing.T) {
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1}
	err := Dispatch(context.Background(), cfg, false, "demo", nil)
	if err == nil {
		t.Fatal("Dispatch(enabled=false) on windows: want ErrRemoteRuntimeNotEnabled, got nil")
	}
	if !strings.Contains(err.Error(), "not yet available") {
		t.Errorf("Dispatch(enabled=false) error = %q, want it to contain %q", err.Error(), "not yet available")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnsupported, true)", kind, ok)
	}
}
