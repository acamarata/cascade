package conversation

// Purpose (this file): the A-T7 error-code table for the three sentinels
//   this ticket adds (ErrMalformedTurnPayload, ErrEgressSubstitutionFailed,
//   ErrSSEWriteFailed) plus ErrSSEUnavailableOnEmbedded, kept separate
//   from adapter.go's method-registration code per the ticket's own
//   300-line-cap note.
//
// CONTRACT DEVIATION (mapping mechanism, recorded, not papered over).
// The ticket text reads as if this file OWNS the JSON-RPC error-code
// mapping ("map them to JSON-RPC error codes via the A-T7 error-code
// table..., consumed by adapter.go's handlers"). The tree has already
// settled this: internal/rpc/registry.go's errorObjectFrom calls
// pkg/cascade.NewRPCError for every non-*rpc.ErrorObject handler error,
// which maps by cascade.Kind through codes.go's frozen RPCCode* table --
// the SAME table every other adapter in this tree (elevation.go,
// fleet/sessions/rpc.go) relies on with zero adapter-local mapping code.
// A second, package-local method->code table here would either (a)
// duplicate that table and drift from it the first time R-14.3 amends a
// Kind's code, or (b) silently override it for exactly these four errors,
// which is the "back door around the taxonomy" this ticket's own trap
// warns against for the append-only invariant, applied here to error
// wire-mapping instead. So this file does not introduce a second mapping
// path: rpcCodeFor below is a read-only reflection of the SAME
// cascade.Kind->RPCCode table (via cascade.NewRPCError), asserted correct
// by adapter_errors_test.go, and mapAdapterError is a documented identity
// pass-through adapter.go's handlers route every returned error through
// -- so a future reviewer sees exactly where the wire-mapping decision is
// made (in cascade, not here) rather than guessing whether this file is
// doing something adapter.go's error returns do not already get for
// free.
//
// SPORT: internal.conversation.adapter_errors/ADDED (P1-E20-W5-S43-T2).

import "github.com/acamarata/cascade/pkg/cascade"

// rpcCodeFor returns the JSON-RPC 2.0 wire code err's cascade.Kind maps
// to (via cascade.NewRPCError, the tree's one mapping path), or
// RPCCodeInternal if err carries no known Kind -- exactly NewRPCError's
// own documented fallback. Exported test surface for
// adapter_errors_test.go; not called by production dispatch, since
// internal/rpc.Registry.Dispatch already performs this exact mapping for
// every handler error (see this file's CONTRACT DEVIATION note).
func rpcCodeFor(err error) int {
	return cascade.NewRPCError(err).Code
}

// mapAdapterError is the identity pass-through adapter.go's handlers
// route every non-nil returned error through, so this file's mapping
// table doc comment has one real call site rather than being dead
// documentation. It changes nothing about err: the actual code (rpcCodeFor,
// same underlying table) is applied later, once, by
// internal/rpc.Registry.Dispatch.
func mapAdapterError(err error) error {
	return err
}
