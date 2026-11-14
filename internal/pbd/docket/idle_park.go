// Package docket provides ticket lifecycle management.
package docket

// IdleState represents the idle parking state of a ticket.
type IdleState string

const (
	// IdleUnparked indicates the ticket is active and not parked.
	IdleUnparked IdleState = "unparked"
	// IdleParked indicates the ticket has been parked due to inactivity.
	IdleParked IdleState = "parked"
)

// TicketIdle tracks the idle parking state and consecutive zero-progress passes.
type TicketIdle struct {
	State      IdleState
	ZeroPasses int
}

// RecordIdlePass records an idle pass for the given ticket. If progressed is
// false, the zero-pass counter increments and the ticket is parked when the
// counter reaches two. If progressed is true, the counter resets to zero.
// Already parked tickets are not modified.
func RecordIdlePass(t *TicketIdle, progressed bool) {
	if t.State == IdleParked {
		return
	}
	if progressed {
		t.ZeroPasses = 0
		return
	}
	t.ZeroPasses++
	if t.ZeroPasses == 2 {
		t.State = IdleParked
	}
}
