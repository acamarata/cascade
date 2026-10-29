package notify

import (
	"context"
	"time"
)

// FailOpen turns accumulation off and returns every held notification to the
// normal queues. The daemon calls it when the tick loop dies on a panic: with
// no Tick left to end an away episode, a router still withholding delivery
// would strand every later notification. It is idempotent, leaves the state
// machine and the journal untouched (a restart replays an open episode as
// Away again) and compiles no digest.
//
// It deliberately does not take c.mu. It runs in the recover path of a panic
// that may have come from a seam called under c.mu, and it touches only the
// router (internally locked; c.deps.Router is never reassigned), so it can
// never wait on a mutex the dead loop might still hold.
func (c *AwayController) FailOpen() {
	c.deps.Router.SetAccumulate(false)
	for _, n := range c.deps.Router.DrainAccumulated() {
		c.deps.Router.requeue(n)
	}
}

// tickSection is Tick's critical section. The deferred unlock keeps c.mu
// from staying held when the presence, admission or journal seam panics.
func (c *AwayController) tickSection(ctx context.Context, now time.Time) (ended string, drained []Notification, state AwayState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if at := c.presenceActivity(); at.After(c.lastActivity) {
		ended, drained = c.observeActivityLocked(ctx, at)
	} else {
		c.advanceLocked(ctx, now)
	}
	return ended, drained, c.state
}

// eventSection is HandleEvent's critical section, panic-safe like tickSection.
func (c *AwayController) eventSection(ctx context.Context, at time.Time) (string, []Notification, AwayState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ended, drained := c.observeActivityLocked(ctx, at)
	return ended, drained, c.state
}
