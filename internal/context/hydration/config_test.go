package hydration

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAnAbsentSectionYieldsTheDefaults pins R-16.6a's "on by default".
// Hydration exists because a session that starts blind to the context
// engine is the problem; making the fix opt-in would leave the problem.
func TestAnAbsentSectionYieldsTheDefaults(t *testing.T) {
	for name, parent := range map[string]interface{}{
		"no [context] table at all": nil,
		"[context] with no hydration sub-table": map[string]interface{}{
			"something_else": true,
		},
		"an explicitly null sub-table": map[string]interface{}{SectionName: nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := LoadSection(parent)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != Default() {
				t.Fatalf("got %+v, want the defaults %+v", got, Default())
			}
			if !got.Enabled {
				t.Error("hydration is off by default")
			}
		})
	}
}

// TestEveryFieldRoundTrips proves each key is actually read, not defaulted
// past. A parser that ignored a key would pass an "absent yields defaults"
// test and fail this one.
func TestEveryFieldRoundTrips(t *testing.T) {
	got, err := LoadSection(map[string]interface{}{SectionName: map[string]interface{}{
		"enabled": false, "budget_tokens": int64(512), "min_score": 0.75, "timeout_seconds": int64(9),
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Config{Enabled: false, BudgetTokens: 512, MinScore: 0.75, Timeout: 9 * time.Second}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestAMalformedSectionIsRefused walks every way a present value can be
// wrong. Each one is a typed refusal, never a silent fall back to the
// default: a user who wrote something there meant it, and quietly
// substituting a value they did not write is worse than telling them.
func TestAMalformedSectionIsRefused(t *testing.T) {
	for name, table := range map[string]interface{}{
		"[context] is not a table":  "hydration = true",
		"the sub-table is a string": map[string]interface{}{SectionName: "yes"},
		"enabled is a string":       map[string]interface{}{SectionName: map[string]interface{}{"enabled": "yes"}},
		"budget is a string":        map[string]interface{}{SectionName: map[string]interface{}{"budget_tokens": "2000"}},
		"budget is zero":            map[string]interface{}{SectionName: map[string]interface{}{"budget_tokens": int64(0)}},
		"budget is negative":        map[string]interface{}{SectionName: map[string]interface{}{"budget_tokens": int64(-1)}},
		"min_score is a string":     map[string]interface{}{SectionName: map[string]interface{}{"min_score": "0.35"}},
		"min_score is above one":    map[string]interface{}{SectionName: map[string]interface{}{"min_score": 1.5}},
		"min_score is negative":     map[string]interface{}{SectionName: map[string]interface{}{"min_score": -0.1}},
		"timeout is zero":           map[string]interface{}{SectionName: map[string]interface{}{"timeout_seconds": int64(0)}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := LoadSection(table)
			if err == nil {
				t.Fatalf("accepted a malformed section and produced %+v", got)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Fatalf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
			}
			if got != (Config{}) {
				t.Fatalf("a refusal still returned a config: %+v", got)
			}
		})
	}
}

// TestTheScoreBoundsAreInclusive pins both ends of the [0,1] range. A
// floor of 0 keeps everything and a floor of 1 keeps only a perfect
// match; both are meaningful settings, so neither is refused.
func TestTheScoreBoundsAreInclusive(t *testing.T) {
	for _, score := range []float64{0, 1} {
		got, err := LoadSection(map[string]interface{}{SectionName: map[string]interface{}{"min_score": score}})
		if err != nil {
			t.Fatalf("min_score %v was refused: %v", score, err)
		}
		if got.MinScore != score {
			t.Fatalf("min_score = %v, want %v", got.MinScore, score)
		}
	}
}

// TestIntegerSpellingsAreAccepted covers the shapes a TOML decoder
// produces for the same written value, so a config that parsed one way on
// one decoder is not refused on another.
func TestIntegerSpellingsAreAccepted(t *testing.T) {
	for name, value := range map[string]interface{}{
		"int": 1500, "int64": int64(1500), "whole float": float64(1500),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := LoadSection(map[string]interface{}{
				SectionName: map[string]interface{}{"budget_tokens": value},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.BudgetTokens != 1500 {
				t.Fatalf("budget_tokens = %d, want 1500", got.BudgetTokens)
			}
		})
	}
}

// TestTheDefaultsAreTheRatifiedConstants pins R-16.6a's numbers. They are
// a ruling, not a preference, so a change to any of them should have to
// go through this line.
func TestTheDefaultsAreTheRatifiedConstants(t *testing.T) {
	d := Default()
	if !d.Enabled || d.BudgetTokens != 2000 || d.MinScore != 0.35 || d.Timeout != 3*time.Second {
		t.Fatalf("defaults = %+v, want enabled/2000/0.35/3s per R-16.6a", d)
	}
}
