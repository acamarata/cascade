// Purpose: the drained-state transition on a device record (R-21.220's
//
//	`node drain <id>` verb, mounted by S-36.T4) and the node.drain RPC
//	handler that wires it to a production caller.
//
// Inputs: a node id already carrying a device record (records.go,
//
//	S-36.T1).
//
// Outputs: the updated DeviceRecord with Drained=true, or a typed
//
//	fail-closed error (unknown node id -> cascade.KindNotFound; an
//	already-drained node is a no-op success, never an error, so a
//	retried `node drain` is idempotent).
//
// Constraints: drain is NOT deletion and NOT revocation: the record, its
//
//	trust_tier, keys and sync cursors are all preserved unchanged. Only
//	the Drained flag moves false->true. S-37.T1's placement filter is the
//	real consumer of this flag; this ticket only implements and exposes
//	the transition (05 §Epic Q S-36.T4).
//
//	NOT AN internal/rpc HANDLER (CONTRADICTION - full quote in the ticket
//	journal). The contract's HOW section names this "the node.drain RPC";
//	the only file that mounts internal/nodes handlers on the node agent's
//	real *rpc.Registry is serve.go's BuildServeRegistry (S-36.T2's file),
//	which this ticket's files_scope does not include in either its add or
//	change list. cmd/cascade may not import internal/rpc at all (the
//	cmd-rpc-server boundary, internal/client/boundary_test.go), so `node
//	drain` cannot dispatch through a registry either. Drain is therefore a
//	plain *RecordStore method the CLI calls directly, matching every other
//	"thin mirror" verb already in this tree (vault_elevated.go's broker.Get/
//	broker.Rotate calls, never an rpc.Registry) rather than a stub RPC
//	surface with no reachable transport.
//
// SPORT: internal/nodes Drain/ADDED (P1-E17-W4-S36-T4).

package nodes

// Drain marks nodeID's device record as drained: it stops being eligible
// for new work placement (S-37.T1) while its enrollment, keys, tier and
// sync cursors are all preserved. Draining an already-drained node is a
// no-op success (idempotent), never a conflict.
func (s *RecordStore) Drain(nodeID string) (DeviceRecord, error) {
	rec, err := s.Get(nodeID)
	if err != nil {
		return DeviceRecord{}, err
	}
	if rec.Drained {
		return rec, nil
	}
	rec.Drained = true
	if err := s.put(rec); err != nil {
		return DeviceRecord{}, err
	}
	return rec, nil
}
