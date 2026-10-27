//go:build !windows

package daemon

// Purpose (this file): presence trusts witnesses from ONE elevation
//   middleware, the issuer the tracker is bound to. A witness minted by another
//   gate (a second middleware with its own issuer) never counts, and a tracker
//   that was never bound, or bound to the zero verifier, counts nothing. Each
//   case has an attested positive control on the bound issuer.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
)

func TestPresenceIgnoresWitnessFromAnotherIssuer(t *testing.T) {
	r := newPresenceRig(t)
	// A second gate over the same custody and trust: valid attestations, but
	// its witnesses carry a different issuer.
	other, _ := NewElevationMiddleware(r.f.dir, selectorOver(r.f.dir, elevation.CustodyPlatform, r.f.ks), r.f.log, r.f.clock)
	r.elev = other
	if _, err := r.attested(okHandler, `{"manifest":"x"}`); err != nil {
		t.Fatalf("attested call through the other gate: %v", err)
	}
	if got := r.tracker.LastActivity(); !got.IsZero() {
		t.Fatalf("a witness from another issuer advanced presence to %v", got)
	}
}

func TestPresenceUnboundTrackerCountsNothing(t *testing.T) {
	r := newPresenceRig(t)
	for name, bind := range map[string]func(*PresenceTracker){
		"never bound":   func(*PresenceTracker) {},
		"zero verifier": func(tr *PresenceTracker) { tr.Bind(rpc.WitnessVerifier{}) },
	} {
		rig := *r
		rig.tracker = NewPresenceTracker(r.clock)
		bind(rig.tracker)
		if _, err := rig.attested(okHandler, `{"manifest":"y"}`); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := rig.tracker.LastActivity(); !got.IsZero() {
			t.Fatalf("%s: tracker counted a witness (%v)", name, got)
		}
	}
	var nilTracker *PresenceTracker
	nilTracker.Bind(rpc.WitnessVerifier{}) // must not panic
	if _, err := PresenceMiddleware(nil)("plugin.add", okHandler)(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}
