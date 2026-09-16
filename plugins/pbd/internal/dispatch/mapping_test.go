package dispatch

import (
	"testing"

	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// Purpose (this file): the §5.18 table-literal parity check. This package
//   cannot import internal/conductor's canonical table (the plugins/→pkg
//   boundary), so the rows are written out verbatim here and every
//   model_class the schema defines is required to appear — which is what
//   makes drift a failure rather than a silent mis-route.
// SPORT: plugins/pbd/internal/dispatch tests (ADD) — P1-E14-W3-S30-T2.

// section518 is 06-FORGE-SPEC §5.18's mapping, transcribed:
//
//	mech → code | build → code | heavy → code | review → review | arbiter → arbitrate
var section518 = map[pews.ModelClass]string{
	pews.ModelClassMech:    "code",
	pews.ModelClassBuild:   "code",
	pews.ModelClassHeavy:   "code",
	pews.ModelClassReview:  "review",
	pews.ModelClassArbiter: "arbitrate",
}

// TestEveryModelClassMapsToItsSpecRow is the parity assertion. It iterates
// the SCHEMA's own five values rather than this file's map keys, so a sixth
// model_class added to pews without a row here fails right here instead of
// being dispatched under a guessed class.
func TestEveryModelClassMapsToItsSpecRow(t *testing.T) {
	all := []pews.ModelClass{
		pews.ModelClassMech, pews.ModelClassBuild, pews.ModelClassHeavy,
		pews.ModelClassReview, pews.ModelClassArbiter,
	}
	for _, m := range all {
		if !m.Valid() {
			t.Fatalf("%q is not a model_class the schema accepts; this list has drifted", m)
		}
		want, spelled := section518[m]
		if !spelled {
			t.Errorf("model_class %q has no §5.18 row transcribed in this test", m)
			continue
		}
		got, ok := TaskClassFor(m)
		if !ok {
			t.Errorf("TaskClassFor(%q) refused a model_class the schema defines", m)
			continue
		}
		if got != want {
			t.Errorf("TaskClassFor(%q) = %q, want §5.18's %q", m, got, want)
		}
	}
	if len(section518) != len(all) {
		t.Errorf("the transcribed table has %d rows for %d model classes", len(section518), len(all))
	}
}

// TestAnUnknownModelClassIsRefusedNotGuessed proves the absent default
// clause is a refusal at runtime too, not just a lint condition. A wrong
// task class routes work to the wrong lane, so a guess is worse than a
// refusal.
func TestAnUnknownModelClassIsRefusedNotGuessed(t *testing.T) {
	for _, m := range []pews.ModelClass{"", "mechanical", "Build", "heavy ", "oracle"} {
		if got, ok := TaskClassFor(m); ok {
			t.Errorf("TaskClassFor(%q) = %q, want a refusal", m, got)
		}
	}
}

// TestTheProducedClassesAreTheOnlyThree pins the output vocabulary: the
// mapping must never produce a fourth class, because K/S-22.T4 owns the
// closed set and this package cannot import it to check.
func TestTheProducedClassesAreTheOnlyThree(t *testing.T) {
	allowed := map[string]bool{TaskClassCode: true, TaskClassReview: true, TaskClassArbitrate: true}
	for m := range section518 {
		got, ok := TaskClassFor(m)
		if !ok {
			t.Fatalf("TaskClassFor(%q) refused", m)
		}
		if !allowed[got] {
			t.Errorf("TaskClassFor(%q) produced %q, which is outside the three §5.18 classes", m, got)
		}
	}
}
