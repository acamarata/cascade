package initconfig

// Purpose: the setup file's `kind` values (P1-E16-W4-S35-T12) — that they
//   match the intake pipeline's declared set, and that an unrecognised one
//   is refused at the FILE with a nearest match.
// Constraints: the file's accepted set is READ from the intake pipeline
//   rather than spelled again here. The first test asserts that, because
//   the failure mode of a second copy is the quiet one -- a kind in one
//   list and not the other parses and then fails at the probe, against a
//   real endpoint.
// SPORT: internal/runtime/initconfig driver kinds (ADD) —
//   P1-E16-W4-S35-T12.

import (
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/intake"
)

// TestTheKindListIsTheIntakePipelineList: the file's accepted set IS the
// pipeline's declared set, not a copy of it. Asserted rather than assumed,
// because the failure mode of a copy is the quiet one — a kind in one list
// and not the other parses here and fails at the probe, several steps
// later and against a real endpoint.
func TestTheKindListIsTheIntakePipelineList(t *testing.T) {
	got := append([]string(nil), driverKinds...)
	want := make([]string, 0, len(intake.DriverKinds()))
	for _, k := range intake.DriverKinds() {
		want = append(want, string(k))
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the setup file accepts %v but the intake pipeline declares %v", got, want)
	}
	if len(got) == 0 {
		t.Error("the kind list is empty, so every pin would be refused")
	}
}

// TestAnUnknownKindIsRefusedAtTheFile: the author learns it from their own
// input, not from a probe failing against a real endpoint several steps
// later.
func TestAnUnknownKindIsRefusedAtTheFile(t *testing.T) {
	_, err := Parse([]byte(`schema = "cascade.init/v1"

[[providers]]
name = "acme"
kind = "openai_compat"
key_env = "ACME_KEY"
`))
	if err == nil {
		t.Fatal("a misspelled kind parsed successfully")
	}
	if !strings.Contains(err.Error(), "openai-compat") {
		t.Errorf("the refusal does not suggest the nearest kind: %v", err)
	}
}

// TestAKindResemblingNothingDrawsNoSuggestion: one bad guess teaches an
// author to ignore every later one, so the refusal names the whole set
// instead of inventing a nearest match.
func TestAKindResemblingNothingDrawsNoSuggestion(t *testing.T) {
	_, err := Parse([]byte(`schema = "cascade.init/v1"

[[providers]]
name = "acme"
kind = "zzzzzzzzzzzzzzzz"
key_env = "ACME_KEY"
`))
	if err == nil {
		t.Fatal("an unrecognisable kind parsed successfully")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("a kind resembling nothing drew a suggestion anyway: %v", err)
	}
	for _, kind := range driverKinds {
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("the refusal does not name %q as an option: %v", kind, err)
		}
	}
}

// TestEveryKnownKindParses keeps the refusal from being over-eager.
func TestEveryKnownKindParses(t *testing.T) {
	for _, kind := range driverKinds {
		if _, err := Parse([]byte(`schema = "cascade.init/v1"

[[providers]]
name = "acme"
kind = "` + kind + `"
key_env = "ACME_KEY"
`)); err != nil {
			t.Errorf("the known kind %q was refused: %v", kind, err)
		}
	}
}
