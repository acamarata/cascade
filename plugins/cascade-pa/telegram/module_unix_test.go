//go:build !windows

// Purpose: the other half of the build-tag pair. Without this file the
//   non-Windows branch of platformBridgeRefusal would be compiled by every
//   lane and asserted by none, which is the shape that lets a platform gate
//   drift into refusing (or admitting) everywhere.
//
// SPORT: plugins/cascade-pa/telegram platform-gate/TEST (P1-E23-W5-S48-T1).

package telegram

import (
	"context"
	"testing"
	"time"
)

func TestPlatformBridgeRefusal_IsAbsentOffWindows(t *testing.T) {
	if refusal := platformBridgeRefusal(); refusal != nil {
		t.Fatalf("platformBridgeRefusal = %v on a platform that runs the daemon", refusal)
	}
}

// TestTelegramStartSucceedsOffWindows is the behavioural counterpart: the same
// Start the windows lane refuses must actually start here, or the refusal test
// would pass on a module that cannot start anywhere.
func TestTelegramStartSucceedsOffWindows(t *testing.T) {
	rig := newDefaultRig(t)
	rig.doer.push(mustReadTestdata(t, "getupdates_text.json"), nil)
	if err := rig.module.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rig.module.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
