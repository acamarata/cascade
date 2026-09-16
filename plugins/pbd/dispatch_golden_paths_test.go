package pbd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose (this file): the branches the golden itself cannot reach — what
//   the seam does when a real ticket is malformed, when the caller gives
//   up, and what keeps the recorded bytes identical on every platform.
//
// Why these are here and not in dispatch_test.go: they are S-30.T4's named
//   acceptance branches, and each one is stated against a REAL fixture
//   rather than a hand-built ticket, so a fixture refresh that breaks one
//   fails here instead of passing a test that no longer resembles the
//   corpus.
//
// Constraints: no network (Art.7.2); no live daemon.
// SPORT: plugins/pbd tests (ADD) — P1-E14-W3-S30-T4.

// TestATicketWithNoTasksIsRefused covers the first named branch. A ticket
// with an empty task list carries no work: dispatching it would send the
// model a bare title, bill a lane for it, and return an empty output that
// reads exactly like a completed ticket.
func TestATicketWithNoTasksIsRefused(t *testing.T) {
	fx := loadGolden(t, "mech.json")
	ticket := ticketOf(fx)
	ticket.Tasks = nil

	caller := &capturingCaller{}
	_, err := NewConductorDispatcher(caller).Dispatch(context.Background(), ticket)
	if err == nil {
		t.Fatal("a ticket with no tasks was dispatched")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
	if caller.method != "" {
		t.Errorf("the door %q was called for a ticket with no work in it", caller.method)
	}
}

// TestSensitivityNeverWidensOnARealTicket covers the second named branch.
// The seam RESOLVES an unresolvable caller tier to restricted; it does not
// omit the field and let the door pick. Asserted on the wire body, because
// that is where a silent widening would actually show up.
func TestSensitivityNeverWidensOnARealTicket(t *testing.T) {
	for _, name := range goldenNames {
		fx := loadGolden(t, name)
		body := assembledRequest(t, fx)
		if !bytes.Contains(body, []byte(`"sensitivity": "restricted"`)) {
			t.Errorf("%s: the wire body does not stamp restricted:\n%s", name, body)
		}
	}
	// And the tier the seam stamps is the ZERO value, so a request built by
	// a future path that forgets to stamp still cannot read as permissive.
	var unset provider.SensitivityTier
	if unset != provider.SensitivityRestricted {
		t.Error("SensitivityTier's zero value is no longer restricted; fail-closed has moved")
	}
}

// TestACancelledContextStopsARealDispatch covers the third named branch: a
// caller that has given up gets context.Canceled, not a late result.
func TestACancelledContextStopsARealDispatch(t *testing.T) {
	fx := loadGolden(t, "heavy.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewConductorDispatcher(&abortingCaller{}).Dispatch(ctx, ticketOf(fx))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestTheFixturesAreByteIdenticalOnEveryPlatform is the Art.5 half.
//
// A golden compared byte for byte is the classic platform-parity trap: a
// Windows checkout with core.autocrlf on rewrites LF to CRLF, and every
// comparison then fails for a reason that has nothing to do with the code.
// The repo's .gitattributes marks `**/testdata/**` as `-text` to prevent
// that; this test is the assertion that the protection is working, and it
// also catches a fixture regenerated on a Windows workstation and committed
// with CRLF already in it — which no checkout attribute can undo.
func TestTheFixturesAreByteIdenticalOnEveryPlatform(t *testing.T) {
	for _, name := range append(append([]string{}, goldenNames...), "README.md") {
		raw, err := os.ReadFile(filepath.Join(goldenDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("\r")) {
			t.Errorf("%s contains a carriage return; the golden will not compare equal across platforms", name)
		}
	}
}
