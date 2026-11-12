package capacity

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/provider"
)

// LearnedInputs holds the learned ranking hooks read for one selection.
type LearnedInputs struct {
	Pairs  economics.LearnedPairsFn
	Refine func(context.Context, string, provider.SensitivityTier, economics.Ranking) (economics.Ranked, error)
}

// LearnedInputReader reads the current hooks, reporting whether they are bound.
// A read error is distinct from an unbound reader.
type LearnedInputReader func() (LearnedInputs, bool, error)
