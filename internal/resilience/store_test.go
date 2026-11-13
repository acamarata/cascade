// Package resilience provides types and interfaces for tracking process health.
package resilience

import (
	"testing"
	"time"
)

// TestStoreTypesCompile verifies that all required types, constants, and
// interface methods exist and have the expected signatures.
func TestStoreTypesCompile(_ *testing.T) {
	// WaitKind constants
	_ = WaitingDisk
	_ = WaitingNet
	_ = WaitingModels
	_ = WaitingOwner

	// Struct field checks
	now := time.Now()

	m := Meta{}
	_ = m

	w := Wait{Since: now, Kind: WaitingDisk}
	_ = w.Since
	_ = w.Kind

	hb := Heartbeat{}
	_ = hb

	r := Restart{Count: 1, Last: now}
	_ = r.Count
	_ = r.Last

	s := Stall{}
	_ = s

	ev := Event{At: now}
	_ = ev.At

	// Store interface compile check via nil pointer
	var st Store
	if st != nil {
		_ = st.SetWait(WaitingDisk)
		_ = st.ClearWait(WaitingNet)
		_ = st.RecordHeartbeat()
		_ = st.MarkSeenRunning()
		_ = st.NextRestartDelay()
		_ = st.RecordStall("test")
		_ = st.EmitEvent(ev)
		_ = st.TrimEvents()
	}
}
