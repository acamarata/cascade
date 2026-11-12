package capacity

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestLearnedInputReaderStates(t *testing.T) {
	failure := cascade.New(cascade.KindInvalidInput, "learned input read failed")
	refine := func(_ context.Context, _ string, _ provider.SensitivityTier, r economics.Ranking) (economics.Ranked, error) {
		return r.Order[0], nil
	}
	for _, bound := range []bool{false, true} {
		reader := LearnedInputReader(func() (LearnedInputs, bool, error) {
			return LearnedInputs{Refine: refine}, bound, nil
		})
		inputs, ok, err := reader()
		if ok != bound || err != nil || inputs.Refine == nil {
			t.Fatalf("read = (%+v, %t, %v), want bound=%t", inputs, ok, err, bound)
		}
	}
	reader := LearnedInputReader(func() (LearnedInputs, bool, error) {
		return LearnedInputs{}, false, failure
	})
	_, _, err := reader()
	if err != failure || err.Error() != "invalid-input: learned input read failed" {
		t.Fatalf("read error = %v, want original error and message", err)
	}
}
