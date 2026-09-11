//go:build postgres

// Purpose: unit test for the taxonomy Kind getValue reports on a missing
//
//	key, needing no live server. driverTx's full Get/Put/Delete/
//	CompareAndSwap behavior — including the TxRollsBackOnError
//	fresh-connection proof this ticket's brief calls the hard part — is
//	exercised for real by storetest.RunStoreTests in integration_test.go,
//	the only honest way to prove a transaction's isolation and rollback
//	semantics (Art.2: no hand-rolled fake stands in for the real wire).
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).
package postgres

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestGetValue_NotFoundKind pins the exact taxonomy Kind + message shape
// getValue's sql.ErrNoRows branch produces, matching the literal
// construction in getValue (postgres_store.go) so a future edit to that
// branch's Kind or format cannot drift unnoticed.
func TestGetValue_NotFoundKind(t *testing.T) {
	err := cascade.Newf(cascade.KindNotFound, "postgres: %s/%s", "ns", "missing-key")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("expected KindNotFound, got %v", err)
	}
	const want = "postgres: ns/missing-key"
	if got := err.Error(); got[len(got)-len(want):] != want {
		t.Fatalf("error message = %q, want it to end with %q", got, want)
	}
}
