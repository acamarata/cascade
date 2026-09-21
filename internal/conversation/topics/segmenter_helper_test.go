package topics

import "github.com/acamarata/cascade/pkg/provider"

// newTestSegmenter is the three-parameter convenience the tests use:
// executor, embedder, HysteresisConfig. Production has no such caller —
// NewDefaultAutoThreader builds ONE Classifier and shares it through
// NewSegmenterWith (P1-E21-W5-S46-T5 D6) — so the wrapper lives here, not
// in the shipped package surface. A nil executor is refused up front, as
// the former production wrapper did, because NewClassifier(nil) is a
// non-nil interface value that NewSegmenterWith's own nil check cannot see.
func newTestSegmenter(executor provider.ModelExecutor, embedder provider.Embedder, cfg HysteresisConfig) (Segmenter, error) {
	if executor == nil {
		return nil, errSegmenterNilExecutor()
	}
	return NewSegmenterWith(NewClassifier(executor), embedder, cfg)
}
