// Package delegation mints, verifies and gates delegation scope envelopes
// (contract:delegation-record, P1-AGT-11).
//
// It owns no envelope or budget type (ADR-0007): MintRoot and MintChild
// build pkg/envelope values from named daemon Sources, MintChild signs only
// what envelope.Narrow accepts against the parent, and VerifyChain walks a
// record stored by internal/jobs to its root before any lease. The caps
// MaxDepth and MaxLiveChildren alias the jobs store's constants, which the
// store enforces atomically on insert. RegisterPreSpawnGate and
// PreSpawnGates are the init-time registry the delegate path runs before
// spawning an agent.
//
// Reference: docs/reference/delegation-record.md.
package delegation
