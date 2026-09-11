package evidence

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestErrorsCarryFrozenKinds(t *testing.T) {
	cases := []struct {
		err  error
		kind cascade.Kind
	}{
		{ErrClaimNotFound, cascade.KindNotFound},
		{ErrEvidenceNotFound, cascade.KindNotFound},
		{ErrClaimWithoutEvidence, cascade.KindInvalidInput},
		{ErrInvalidLocator, cascade.KindInvalidInput},
		{ErrClaimInvalidated, cascade.KindConflict},
		{ErrDataClassImmutable, cascade.KindConflict},
		{ErrContentHashMismatch, cascade.KindIntegrity},
		{ErrEvidenceStale, cascade.KindIntegrity},
	}
	for _, tc := range cases {
		if !cascade.HasKind(tc.err, tc.kind) {
			t.Errorf("%v: want kind %v", tc.err, tc.kind)
		}
	}
}
