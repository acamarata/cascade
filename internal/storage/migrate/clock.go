package migrate

import "time"

// Purpose: injectable time source for the ledger's applied_at timestamps.
// Inputs: none (Clock is a zero-arg accessor interface).
// Outputs: the current instant, per the concrete implementation.
// Constraints: 02-TARGET-STRUCTURE §v1.1 / forbidigo forbid bare time.Now
//
//	in domain logic, so this package declares no Clock implementation of
//	its own — a bare time.Now() call site would need a forbidigo
//	suppression this ticket has no standing to add (.golangci.yml is
//	outside files_scope). This is a duck-typed twin of
//	internal/runtime.Clock and internal/testkit.Clock: any *runtime.
//	SystemClock (production) or *testkit.FrozenClock (tests) already
//	satisfies this interface with zero adapter code, since Go interfaces
//	are structural and all three declare exactly Now() time.Time.
//
// SPORT: internal.storage.migrate.MigrationBuilder/ADDED (P1-E02-W1-S02-T3).

// Clock abstracts time.Now so Apply never reads the wall clock directly.
// Pass internal/runtime.NewSystemClock() (production) or an
// internal/testkit.FrozenClock (tests) — both already satisfy this
// interface structurally.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}
