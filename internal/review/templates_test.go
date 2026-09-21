package review

import (
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestTemplateTaskClassMatchesRealTaxonomy grounds CR-A/CR-B/CR-C's
// task_class against the REAL §5.16 table (conductor.TaskClasses()) rather
// than a second, hand-typed literal -- the same grounding style
// internal/context/pipeline_lane_test.go uses. A row rename in the real
// taxonomy turns this red instead of silently drifting.
func TestTemplateTaskClassMatchesRealTaxonomy(t *testing.T) {
	rows := conductor.TaskClasses()
	classNames := map[string]bool{}
	for _, r := range rows {
		classNames[r.Class] = true
	}
	if !classNames[string(conductor.TaskClassReview)] {
		t.Fatalf("the real §5.16 table has no %q row", conductor.TaskClassReview)
	}
	if !classNames[string(conductor.TaskClassArbitrate)] {
		t.Fatalf("the real §5.16 table has no %q row", conductor.TaskClassArbitrate)
	}
	if got := NewCRATemplate().TaskClass(); got != conductor.TaskClassReview {
		t.Errorf("CR-A task_class = %q, want %q", got, conductor.TaskClassReview)
	}
	if got := NewCRBTemplate().TaskClass(); got != conductor.TaskClassReview {
		t.Errorf("CR-B task_class = %q, want %q", got, conductor.TaskClassReview)
	}
	if got := NewCRCTemplate().TaskClass(); got != conductor.TaskClassArbitrate {
		t.Errorf("CR-C task_class = %q, want %q", got, conductor.TaskClassArbitrate)
	}
}

// TestTemplateSnapshotGolden golden-tests each level's serialized form
// byte-for-byte, so a field rename or a value drift in NewCRATemplate/
// NewCRBTemplate/NewCRCTemplate turns this red rather than silently
// shipping (the ticket's own "golden-test the serialized form" task).
func TestTemplateSnapshotGolden(t *testing.T) {
	cases := []struct {
		name string
		tmpl Template
		want string
	}{
		{"CR-A", NewCRATemplate(),
			`{"level":"CR-A","task_class":"review","reasoning":"medium","context_k":32,"structured":true,"sensitivity":"restricted","rubric":"CR-A lightweight review: inspect the artifact for per-file or per-hunk defects only. Emit structured findings; do not evaluate architecture."}`},
		{"CR-B", NewCRBTemplate(),
			`{"level":"CR-B","task_class":"review","reasoning":"high","context_k":200,"structured":true,"sensitivity":"restricted","rubric":"CR-B peer review: inspect the full artifact and its context. Emit structured findings ranked by severity, each with a verdict."}`},
		{"CR-C", NewCRCTemplate(),
			`{"level":"CR-C","task_class":"arbitrate","reasoning":"max","context_k":200,"structured":true,"sensitivity":"restricted","rubric":"CR-C adversarial review, PROPOSE pass: produce a candidate architecture/security verdict over the artifact with supporting findings."}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.tmpl.Snapshot())
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("serialized form drifted:\n got:  %s\n want: %s", got, tc.want)
			}
		})
	}
}

// TestTemplateForEveryLevel proves TemplateFor resolves every valid level
// and refuses an invalid one with a typed error -- not a panic, not a zero
// value passed off as success.
func TestTemplateForEveryLevel(t *testing.T) {
	for _, level := range []provider.ReviewCRLevel{provider.ReviewCRLevelA, provider.ReviewCRLevelB, provider.ReviewCRLevelC} {
		if _, err := TemplateFor(level); err != nil {
			t.Errorf("TemplateFor(%q) = %v, want nil error", level, err)
		}
	}
	if _, err := TemplateFor(provider.ReviewCRLevel("CR-Z")); err == nil {
		t.Error("TemplateFor(\"CR-Z\") returned nil error, want a typed refusal")
	}
}

// TestTemplateFieldsComeFromTheRealTaxonomyRow is the CR fix D1 grounding:
// reasoning, context, structured AND sensitivity are asserted against the
// REAL §5.16 row for each level's task class, not against literals restated
// here. The draft's golden test grounded only the class NAME, so its
// `internal` sensitivity -- a downgrade from the review row's restricted
// default on every dispatch -- was golden-PINNED rather than caught.
func TestTemplateFieldsComeFromTheRealTaxonomyRow(t *testing.T) {
	rows := conductor.TaskClasses()
	cases := []struct {
		level     provider.ReviewCRLevel
		class     conductor.TaskClass
		tmpl      Template
		cheaperOK bool
	}{
		{provider.ReviewCRLevelA, conductor.TaskClassReview, NewCRATemplate(), true},
		{provider.ReviewCRLevelB, conductor.TaskClassReview, NewCRBTemplate(), false},
		{provider.ReviewCRLevelC, conductor.TaskClassArbitrate, NewCRCTemplate(), false},
	}
	for _, tc := range cases {
		row, ok := rowFor(rows, tc.class)
		if !ok {
			t.Fatalf("the real §5.16 table has no %q row", tc.class)
		}
		if got := tc.tmpl.Sensitivity(); got != row.SensitivityDefault {
			t.Errorf("%s Sensitivity() = %v, want the %q row's own SensitivityDefault %v -- never below it",
				tc.level, got, tc.class, row.SensitivityDefault)
		}
		if got := tc.tmpl.Requirements().Structured; got != row.Structured {
			t.Errorf("%s Requirements().Structured = %v, want the row's %v", tc.level, got, row.Structured)
		}
		if tc.cheaperOK {
			// CR-A's single documented deviation: lighter reasoning and
			// context than the row, never a lighter sensitivity tier.
			if tc.tmpl.Requirements().Reasoning != "medium" || tc.tmpl.Requirements().Context != 32000 {
				t.Errorf("CR-A Requirements() = %+v, want the ticket's own {medium, 32000}", tc.tmpl.Requirements())
			}
			continue
		}
		if got := tc.tmpl.Requirements(); got.Reasoning != row.Reasoning || got.Context != row.CtxK*1000 {
			t.Errorf("%s Requirements() = %+v, want the row's own {%s, %d}",
				tc.level, got, row.Reasoning, row.CtxK*1000)
		}
	}
}

// TestTemplateRequirementsAreTokenCounts asserts Requirements().Context
// reports raw tokens (32000, not 32) -- a caller matching against a lane's
// advertised context window needs the real unit, not the ticket prose's "k"
// shorthand.
func TestTemplateRequirementsAreTokenCounts(t *testing.T) {
	if got := NewCRATemplate().Requirements().Context; got != 32000 {
		t.Errorf("CR-A Requirements().Context = %d, want 32000", got)
	}
	if got := NewCRBTemplate().Requirements().Context; got != 200000 {
		t.Errorf("CR-B Requirements().Context = %d, want 200000", got)
	}
	if got := NewCRCTemplate().Requirements().Context; got != 200000 {
		t.Errorf("CR-C Requirements().Context = %d, want 200000", got)
	}
}
