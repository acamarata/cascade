package remote

// Purpose: Interceptor, the egress-firewall seam dialRemote calls before
//   any handshake byte leaves the process (06-FORGE-SPEC §5.17, R-21.265,
//   R-21.228: "route every handshake byte through Intercept(ctx,
//   EgressClassPluginRemote, tier, content) with the tier passed
//   explicitly").
//
// WHY THIS FILE DOES NOT IMPORT internal/hooks/egress (contract
// deviation, recorded per LANE-RULES §1 — see remote.go's package-doc
// BOUNDARY NOTE for the full depguard argument): this ticket's own task
// list says "call RegisterClass from internal/plugins/remote/egress.go
// package init" and "obtain the egress.Capability and route every
// handshake byte through Intercept(...)" directly in this package. Both
// are impossible here: .golangci.yml's plugins-providers-boundary rule
// denies every non-test file under internal/plugins/remote/ from
// importing internal/** at all, with no carve-out for this file. Two
// further facts make following the task text as literally written wrong
// even if the boundary did not exist: (1) EgressClassPluginRemote is
// ALREADY registered — internal/hooks/egress/classes.go's defaultClasses
// table carries {EgressClassPluginRemote, InterceptConfig{Enabled: false,
// Owner: "O/S-33.T4"}} — so a second Register call from this package's
// own init would return ErrDuplicateClass and panic (classes.go's
// MustRegister panics on any registration failure); (2) no production
// composition root anywhere in this tree constructs an *egress.Engine at
// all (grepped for "egress.NewEngine" and "InterceptClass(" across
// internal/ and cmd/: zero production call sites), so there is no live
// Capability for this package to "obtain" even if it could import the
// package that issues one.
//
// The real wiring lives in internal/plugins/dispatch.go instead — the
// one file in this package tree the boundary exempts, and the file this
// ticket's own files_scope already lists as one it changes: dispatch.go
// acquires the real egress.Capability for EgressClassPluginRemote from
// egress.DefaultRegistry(), builds an Interceptor adapter around it, and
// passes that adapter into Dispatch (remote.go). This file only
// declares the shape that adapter must satisfy.
//
// Inputs: none (interface declaration only).
// Outputs: the Interceptor type.
// Constraints: pkg-only imports (BOUNDARY NOTE).
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import "context"

// Interceptor is the egress-firewall seam: content is what dialRemote is
// about to write to the wire, and the returned bytes are what is
// actually safe to send (or an error and nothing to send). A production
// Interceptor wraps a real *egress.Engine bound to the unforgeable
// egress.Capability for EgressClassPluginRemote and a declared
// SensitivityTier; a test double may be a pass-through or a recording
// stub.
//
// A nil Interceptor is refused by dialRemote (fail-closed) rather than
// treated as "skip the firewall" — see remote.go's dialRemote.
type Interceptor interface {
	// Intercept filters content before it is written to the wire,
	// returning the bytes that are actually safe to send.
	Intercept(ctx context.Context, content []byte) ([]byte, error)
}
