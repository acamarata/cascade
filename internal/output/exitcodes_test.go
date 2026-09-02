// Purpose: tests for exitcodes.go — proves ExitCode is pure delegation to
//
//	pkg/cascade's frozen R-14.3 table (codes.go/wire.go), never a second
//	copy of it. TestExitCodeTable is the name the ticket's checks list
//	pins (`go test -run TestExitCodeTable ./internal/output/...`).
//
// SPORT: internal/output [ADD] (D/S-06.T5 sport_updates).
package output_test

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestExitCodeTable proves output.ExitCode is total, exhaustive delegation
// over the frozen 14-kind taxonomy plus the nil/non-taxonomy edge cases —
// a delegation proof, not a second exhaustiveness/non-collision test of
// the table's CONTENT (pkg/cascade/wire_test.go's
// TestExitCodeTable_MatchesR143 and TestTaxonomyTablesTotalAndNonOverlapping
// already own that; re-deriving it here would be the duplicate-table
// defect exitcodes.go's own doc comment explains this ticket avoided).
func TestExitCodeTable(t *testing.T) {
	if got := output.ExitCode(nil); got != cascade.ExitOK {
		t.Errorf("ExitCode(nil) = %d, want ExitOK (%d)", got, cascade.ExitOK)
	}

	seen := map[int]cascade.Kind{}
	for _, k := range cascade.AllKinds() {
		err := cascade.New(k, "x")

		got := output.ExitCode(err)
		if want := k.ExitCode(); got != want {
			t.Errorf("kind %v: output.ExitCode = %d, want %d (k.ExitCode())", k, got, want)
		}
		if want := cascade.ExitCode(err); got != want {
			t.Errorf("kind %v: output.ExitCode = %d, want %d (cascade.ExitCode())", k, got, want)
		}

		// No two kinds may share a code, and no error kind may map to
		// ExitOK (0) — the ticket's own conformance requirement, verified
		// here as a property of the delegated values rather than a
		// second table.
		if got == cascade.ExitOK {
			t.Errorf("kind %v: ExitCode = ExitOK (0), an error kind must never map to success", k)
		}
		if prior, ok := seen[got]; ok {
			t.Errorf("kind %v and %v collide on exit code %d", k, prior, got)
		}
		seen[got] = k
	}

	plain := errors.New("boom")
	if got, want := output.ExitCode(plain), cascade.ExitInternal; got != want {
		t.Errorf("ExitCode(non-taxonomy error) = %d, want ExitInternal (%d)", got, want)
	}

	canceled := cascade.New(cascade.KindCanceled, "sigint")
	if got := output.ExitCode(canceled); got != 130 {
		t.Errorf("ExitCode(canceled) = %d, want 130 (SIGINT convention)", got)
	}
}
