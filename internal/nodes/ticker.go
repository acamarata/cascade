// Purpose: Ticker, the periodic-notification seam RunHeartbeatLoop,
//	Prober.Run and NetworkWatcher.Run all drive their loops through
//	(Art.7.3 — no bare time.Sleep as synchronization in domain logic).
// Inputs: a tick period (NewSystemTicker) or nothing (tests inject a
//	fake Ticker directly).
// Outputs: a channel that fires once per tick, coalesced (a slow
//	consumer never backs up more than one pending tick).
// Constraints: extracted out of heartbeat.go (P1-E17-W4-S36-T2's file)
//	into its own file so heartbeat.go's presence-transition wiring
//	(P1-E36-W7-S72-T2) fits under the repo's 300-line cap without moving
//	any behavior — Ticker/systemTicker/NewSystemTicker are unchanged,
//	byte-for-byte, from where heartbeat.go originally defined them.
// SPORT: internal/nodes Ticker/MOVED, NewSystemTicker/MOVED
//	(P1-E36-W7-S72-T2, out of heartbeat.go, same package, same API).

package nodes

import "time"

// Ticker abstracts periodic notification (duck-typed against
// internal/runtime.Ticker, mirroring records.go's Clock duck-type) so
// RunHeartbeatLoop, Prober.Run and NetworkWatcher.Run never block on a
// real sleep in tests.
type Ticker interface {
	C() <-chan struct{}
	Stop()
}

// systemTicker is the production Ticker (mirrors internal/runtime's own
// systemTicker; duplicated rather than exported cross-package for one
// two-method helper).
type systemTicker struct {
	t *time.Ticker
	c chan struct{}
}

// NewSystemTicker returns the production Ticker, firing every d (d must
// be positive). Only production entrypoints should call this; tests
// inject a fake Ticker instead (heartbeat_test.go's fakeTicker).
func NewSystemTicker(d time.Duration) Ticker {
	st := &systemTicker{t: time.NewTicker(d), c: make(chan struct{}, 1)}
	go func() {
		for range st.t.C {
			select {
			case st.c <- struct{}{}:
			default:
			}
		}
	}()
	return st
}

func (s *systemTicker) C() <-chan struct{} { return s.c }
func (s *systemTicker) Stop()              { s.t.Stop() }
