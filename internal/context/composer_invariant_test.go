package context

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Purpose: the bounded-context invariant test this ticket exists to
//   produce: TokensUsed never exceeds Budget, over every fixture entry in
//   testdata/composer-fixtures/valid-fixtures.yaml AND over a large
//   generated matrix of (tier size × memory-candidate count × retrieval-hit
//   count) combinations, including combinations far larger than the
//   budget. It also proves the invariant-checking helper itself is load-
//   bearing against testdata/composer-fixtures/violation-fixture.yaml.
// SPORT: context-engine/composer-invariant-test (ADD, per T-1 sport_updates).

// slotFixture and casesFixture mirror valid-fixtures.yaml's shape.
type slotFixture struct {
	Kind   string `yaml:"kind"`
	Label  string `yaml:"label"`
	Tokens int    `yaml:"tokens"`
}

type caseFixture struct {
	Name   string        `yaml:"name"`
	Budget int           `yaml:"budget"`
	Slots  []slotFixture `yaml:"slots"`
}

type casesFixture struct {
	Cases []caseFixture `yaml:"cases"`
}

type violationFixture struct {
	Name              string `yaml:"name"`
	Budget            int    `yaml:"budget"`
	ClaimedTokensUsed int    `yaml:"claimed_tokens_used"`
}

// fixtureCounter measures a slot's Content by reading back the
// "|tokens=N" marker this test embeds when it builds Content from a
// fixture's declared size, so the declared size and the measured size are
// exactly the same number by construction -- no drift between the fixture
// and what Compose actually sees.
type fixtureCounter struct{}

func (fixtureCounter) Count(_ context.Context, text string) (int, error) {
	idx := strings.LastIndex(text, "|tokens=")
	if idx < 0 {
		return 0, nil
	}
	n, err := strconv.Atoi(text[idx+len("|tokens="):])
	if err != nil {
		return 0, fmt.Errorf("fixtureCounter: unparsable marker in %q: %w", text, err)
	}
	return n, nil
}

// slotFromFixture builds a Slot whose Content carries fixtureCounter's
// marker, never real content.
func slotFromFixture(sf slotFixture) Slot {
	kind := map[string]SlotKind{
		"tier": SlotKindTier, "memory": SlotKindMemory,
		"retrieval": SlotKindRetrieval, "history": SlotKindHistory,
	}[sf.Kind]
	return Slot{Kind: kind, Label: sf.Label, Content: fmt.Sprintf("%s|tokens=%d", sf.Label, sf.Tokens)}
}

// boundedContextHolds is the invariant assertion itself: TokensUsed must
// never exceed Budget. Kept as its own named helper (rather than an inline
// comparison at each call site) so the seeded-violation test below can
// exercise the exact same logic the fixture-driven loop relies on.
func boundedContextHolds(res ComposedResult) bool {
	return res.TokensUsed <= res.Budget
}

func TestBoundedContextInvariantFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "composer-fixtures", "valid-fixtures.yaml"))
	if err != nil {
		t.Fatalf("reading valid-fixtures.yaml: %v", err)
	}
	var fx casesFixture
	if err := yaml.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parsing valid-fixtures.yaml: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("valid-fixtures.yaml has no cases (fail closed: an empty table tests nothing)")
	}
	c := mustComposer(t, fixtureCounter{}, nil)
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var slots []Slot
			for _, sf := range tc.Slots {
				slots = append(slots, slotFromFixture(sf))
			}
			res, err := c.Compose(context.Background(), tc.Budget, slots)
			if err != nil {
				return // a typed refusal never emits an over-budget slice.
			}
			if !boundedContextHolds(res) {
				t.Fatalf("bounded-context invariant violated: TokensUsed=%d > Budget=%d",
					res.TokensUsed, res.Budget)
			}
		})
	}
}

// TestSeededViolationFixtureProvesCheckerIsLoadBearing loads
// violation-fixture.yaml and constructs a ComposedResult DIRECTLY from its
// numbers, bypassing Compose entirely (Compose never produces this shape).
// It then requires boundedContextHolds to report the violation. If this
// test itself ever reported the fixture as bounded, the checker used by
// every other test in this file would not be enforcing anything.
func TestSeededViolationFixtureProvesCheckerIsLoadBearing(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "composer-fixtures", "violation-fixture.yaml"))
	if err != nil {
		t.Fatalf("reading violation-fixture.yaml: %v", err)
	}
	var vf violationFixture
	if err := yaml.Unmarshal(raw, &vf); err != nil {
		t.Fatalf("parsing violation-fixture.yaml: %v", err)
	}
	if vf.ClaimedTokensUsed <= vf.Budget {
		t.Fatalf("violation-fixture.yaml is not actually a violation: claimed=%d budget=%d",
			vf.ClaimedTokensUsed, vf.Budget)
	}
	seeded := ComposedResult{TokensUsed: vf.ClaimedTokensUsed, Budget: vf.Budget}
	if boundedContextHolds(seeded) {
		t.Fatalf("boundedContextHolds passed a seeded violation (used=%d budget=%d): "+
			"the checker is not load-bearing", vf.ClaimedTokensUsed, vf.Budget)
	}
}

// TestBoundedContextInvariantGenerated sweeps a deterministic matrix of
// (tier size × memory-candidate count × retrieval-hit count) combinations,
// at budgets from far smaller than the content to far larger, and asserts
// the invariant over every one -- hundreds of generated cases beyond the
// curated fixture table above.
func TestBoundedContextInvariantGenerated(t *testing.T) {
	c := mustComposer(t, fixtureCounter{}, nil)
	tierSizes := []int{0, 1, 10, 500}
	memCounts := []int{0, 1, 3}
	retrievalCounts := []int{0, 1, 5}
	budgets := []int{1, 5, 20, 100, 5000}
	total := 0
	for _, tierSize := range tierSizes {
		for _, memCount := range memCounts {
			for _, retrievalCount := range retrievalCounts {
				for _, budget := range budgets {
					total++
					slots := generatedSlots(tierSize, memCount, retrievalCount)
					res, err := c.Compose(context.Background(), budget, slots)
					if err != nil {
						continue
					}
					if !boundedContextHolds(res) {
						t.Fatalf("tierSize=%d memCount=%d retrievalCount=%d budget=%d: "+
							"TokensUsed=%d > Budget=%d", tierSize, memCount, retrievalCount,
							budget, res.TokensUsed, res.Budget)
					}
				}
			}
		}
	}
	if total < 100 {
		t.Fatalf("generated matrix only produced %d cases, want >= 100", total)
	}
}

// generatedSlots builds one deterministic slot list for the generated
// matrix: one tier slot of tierSize tokens (skipped if 0), memCount memory
// candidates of 7 tokens each, and retrievalCount retrieval hits of 11
// tokens each.
func generatedSlots(tierSize, memCount, retrievalCount int) []Slot {
	var slots []Slot
	if tierSize > 0 {
		slots = append(slots, Slot{Kind: SlotKindTier, Label: "gci",
			Content: fmt.Sprintf("gci|tokens=%d", tierSize)})
	}
	for i := 0; i < memCount; i++ {
		slots = append(slots, Slot{Kind: SlotKindMemory, Label: fmt.Sprintf("soul-%d", i),
			Content: fmt.Sprintf("soul-%d|tokens=7", i)})
	}
	for i := 0; i < retrievalCount; i++ {
		slots = append(slots, Slot{Kind: SlotKindRetrieval, Label: fmt.Sprintf("chunk-%d", i),
			Content: fmt.Sprintf("chunk-%d|tokens=11", i)})
	}
	return slots
}
