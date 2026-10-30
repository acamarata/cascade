package delegation

// Purpose: the pre-spawn gate registry (contract:delegation-record).
//
//	A package registers a PreSpawnGate at init time; the delegate path
//	runs PreSpawnGates() after the marker is bound and before Shim.Spawn,
//	and any refusal aborts the spawn. P1-EXE-04 registers its budget debit
//	here.
//
// SPORT: delegation/gate/ADD (P1-AGT-11).

import (
	"context"
	"sort"
	"sync"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/envelope"
)

// PreSpawnGate checks one delegation before its agent spawns; a non-nil
// error refuses the spawn.
type PreSpawnGate func(ctx context.Context, rec jobs.DelegationRecord, env envelope.Envelope) error

// gateRegistry maps a gate name to its gate.
var gateRegistry = struct {
	sync.Mutex
	m map[string]PreSpawnGate
}{m: map[string]PreSpawnGate{}}

// RegisterPreSpawnGate registers g under name at init time; the struct{}
// result lets a package write `var _ = RegisterPreSpawnGate(...)`. An
// empty name, a nil gate or a duplicate name panics.
func RegisterPreSpawnGate(name string, g PreSpawnGate) struct{} {
	if name == "" || g == nil {
		panic("delegation: RegisterPreSpawnGate needs a name and a gate")
	}
	gateRegistry.Lock()
	defer gateRegistry.Unlock()
	if _, dup := gateRegistry.m[name]; dup {
		panic("delegation: pre-spawn gate " + name + " is already registered")
	}
	gateRegistry.m[name] = g
	return struct{}{}
}

// PreSpawnGates returns every registered gate, sorted by name.
func PreSpawnGates() []PreSpawnGate {
	gateRegistry.Lock()
	defer gateRegistry.Unlock()
	names := make([]string, 0, len(gateRegistry.m))
	for n := range gateRegistry.m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]PreSpawnGate, 0, len(names))
	for _, n := range names {
		out = append(out, gateRegistry.m[n])
	}
	return out
}
