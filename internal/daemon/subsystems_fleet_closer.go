package daemon

// Purpose: the fleet runtime's late-closer rule. A builder step registers the
//
//	teardown of what it opened. Close can run before the step registers it
//	(a daemon stop during start, or the sampler body closing the runtime
//	while a later step still builds); a teardown registered after Close ran
//	would never run, and the open sqlite handle would leak (windows cannot
//	delete a file that is still open). addOrRunCloser closes that gap
//	without a second close path.
//
// Inputs: a teardown func. Outputs: whether it ran at once, and its error.
// Constraints: subsystems_fleet.go (Close, addCloser) stays unchanged; this
// file only reads r.closed under r.mu.
//
// SPORT: internal/daemon (ADD, P1-CI-22).

import (
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// afterJobsDBOpen is a test seam run by buildReservation between opening the
// jobs DB and registering its closer: the window a racing Close wins.
var afterJobsDBOpen = func(*FleetRuntime) {}

// addOrRunCloser registers fn like addCloser while the runtime is open. When
// Close already ran (or is running: it took its snapshot of the closers
// before fn existed) fn is not registered, since no Close would run it: it
// runs at once, exactly once, on the caller's goroutine. ranNow reports that
// case; err is fn's error. The check and the append share r.mu with Close's
// snapshot, so fn runs either from Close's snapshot or here, never both and
// never neither.
func (r *FleetRuntime) addOrRunCloser(fn func() error) (ranNow bool, err error) {
	r.mu.Lock()
	if !r.closed {
		r.closers = append(r.closers, fn)
		r.mu.Unlock()
		return false, nil
	}
	r.mu.Unlock()
	return true, fn()
}

// errClosedDuringBuild is the failure of a build step that lost the race with
// Close. The step has already torn down what it opened.
var errClosedDuringBuild = cascade.New(cascade.KindUnavailable, "fleet runtime: closed while the step was building")

// registerOrFail registers fn through addOrRunCloser. When Close already ran
// it returns errClosedDuringBuild joined with fn's own error, so the step
// fails closed with its handle closed.
func (r *FleetRuntime) registerOrFail(fn func() error) error {
	ranNow, err := r.addOrRunCloser(fn)
	if !ranNow {
		return nil
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, errors.Join(errClosedDuringBuild, err), "fleet runtime: closer ran at once")
	}
	return errClosedDuringBuild
}
