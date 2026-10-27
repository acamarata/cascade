// Package notify is the core notification router, handling aggregation,
// priority, and deep links.
//
// Away mode reads two injected seams: PresenceSource (when the operator was
// last seen) and StallSource (which stalled sessions need attention).
// BusStallSource is the StallSource over the event bus; see docs/away-mode.md.
package notify
