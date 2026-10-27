//go:build !windows

// Purpose: registers audit.gate_append, the daemon verb a CLI process uses
// to write its local elevation gate rows while the daemon holds the store.
// Inputs: the daemon wiring's store, clock and bus.
// Outputs: audit.gate_append bound to gateAppendHandler (gate_audit_append.go).
// Constraints: owner only (the access table declares it); not elevated: it
// grants nothing and only records. The params must be exactly one gateRow
// (unknown fields, trailing data, oversized params and any value outside the
// row vocabulary are refused). Rows are appended through the store's one
// audit chain with no redactor (a gate row has no free text), marked
// source cli-gate. Admission is a token bucket (gateAppendPerSecond, burst
// gateAppendBurst).
// SPORT: cmd/cascade daemon registrations (audit.gate_append, P1-SEC-08).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "audit-gate-append", Phase: phaseNamespaces, Order: 41,
	Wire: func(w *daemonWiring) error {
		if w.Store == nil {
			return nil
		}
		w.Registry.Register(gateAppendMethod, gateAppendHandler(openGateAuditLog(w.Store, w.Clock, w.Bus), w.Clock))
		return nil
	},
})
