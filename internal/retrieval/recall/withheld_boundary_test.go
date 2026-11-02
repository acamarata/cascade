package recall

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
)

// boundaryLeg models a driver returning a distinct unauthorized candidate.
type boundaryLeg struct {
	strategy rrf.StrategyName
	id       string
}

func (l boundaryLeg) Query(context.Context, *fusion.ScopeFilter, string, int) (rrf.RankedList, bool, error) {
	return rrf.RankedList{Strategy: l.strategy, Weight: rrf.NeutralWeight,
		Hits: []rrf.Candidate{{ChunkID: l.id, Path: "excluded/private.md", CorpusID: "journal"}}}, true, nil
}

func TestRecallWithheldCountCombined(t *testing.T) {
	isolateBoundaryHome(t)
	legs := []Leg{boundaryLeg{rrf.StrategyFTS, "excluded-fts"}, boundaryLeg{rrf.StrategyVector, "excluded-vector"}}
	sum := 0
	for _, leg := range legs {
		got, err := newTestService(t, leg).Query(context.Background(), inScopeRequest())
		if err != nil || got.Withheld != 1 {
			t.Fatalf("single leg: withheld=%d err=%v", got.Withheld, err)
		}
		sum += got.Withheld
	}
	got, err := newTestService(t, legs...).Query(context.Background(), inScopeRequest())
	if err != nil || got.Withheld != sum || len(got.Results) != 0 || len(got.Citations) != 0 {
		t.Fatalf("combined: response=%+v err=%v want withheld=%d", got, err, sum)
	}
	if len(got.Legs) != 2 || got.Rendered != "" {
		t.Fatalf("unexpected output for excluded candidates: %+v", got)
	}
}

func TestRecallAllExcludedReturnsEmpty(t *testing.T) {
	isolateBoundaryHome(t)
	svc := newTestService(t, lexicalLeg{}, boundaryLeg{rrf.StrategyVector, "excluded"})
	control, err := svc.Query(context.Background(), inScopeRequest())
	if err != nil || len(control.Results) == 0 {
		t.Fatalf("control must retrieve indexed content: %+v err=%v", control, err)
	}
	req := inScopeRequest()
	req.Scope = "project/unrelated"
	got, err := svc.Query(context.Background(), req)
	if err != nil || len(got.Results) != 0 || len(got.Citations) != 0 || got.Withheld <= 0 || got.Rendered != "" {
		t.Fatalf("all excluded: response=%+v err=%v", got, err)
	}
}

func isolateBoundaryHome(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
}
