// Purpose: the audit side of the local elevation gate that needs no RPC
//
//	types: the vault.access row a vault read writes first, refusals decided
//	outside Authorize, and the production gate wiring. The elevation rows
//	(gateAuditor) live in vault_elevated_gate.go, the one cmd file allowed to
//	import internal/rpc; the row vocabulary is in gate_audit_row.go.
//
// Inputs: an audit.Writer (the process log in production, a test writer
//
//	over a memory store in tests).
//
// Outputs: RecordVaultAccess, RecordRefusal and productionElevationGate.
//
// Constraints: an append failure refuses and is returned, never dropped.
//
// SPORT: cmd/cascade/vault elevation gate audit (P1-SEC-08).

package main

import (
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RecordVaultAccess writes the vault.access row for a requested read (the
// secret's name and the verb, never its value) BEFORE anything is read. The
// row goes through the gate sink, which reads no vault. An append failure
// refuses with the reason code audit_unavailable and nothing is read.
func (g *elevationGate) RecordVaultAccess(ctx context.Context, name, verb string) error {
	if g.sink == nil {
		return cascade.New(cascade.KindElevationRequired, "vault: no audit log is configured to record the access (audit_unavailable)")
	}
	row := gateRow{Kind: string(audit.KindVaultAccess), Verb: verb, Name: name, ParamsHash: audit.HashParams([]byte(name))}
	if _, err := g.sink.AppendGateRow(ctx, row); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "vault: the access could not be audited (audit_unavailable); nothing was read")
	}
	return nil
}

// RecordRefusal records a refusal decided outside Authorize (a command's
// own no-input or daemon-required check) as an attempt then a deny with
// reason, and returns refusal (with any audit failure attached).
func (g *elevationGate) RecordRefusal(ctx context.Context, verb, reason string, refusal error) error {
	_, hash, err := localChallenge(verb)
	if err != nil {
		return refusal
	}
	return (&gateAuditor{sink: g.sink}).refuse(ctx, verb, hash, reason, refusal)
}

// productionElevationGate wires the selected custody, the profile's data
// directory and the gate sink (the daemon when one runs, else the store).
func productionElevationGate(paths runtime.PathProvider, getenv runtime.Getenv) *elevationGate {
	clock := runtime.NewSystemClock()
	return newElevationGate(productionCustody, paths.DataDir, &autoGateSink{paths: paths, clock: clock}, clock, getenv)
}
