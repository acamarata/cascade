// Purpose: TestResourceRequestRankFields (P1-TOP-09a), the rank fields
// Roles, Producer and IndependenceWeight on ResourceRequest, split from
// request_test.go under the 300-line file cap.

package capacity

import (
	"math"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestResourceRequestRankFields proves ResourceRequest gained exactly
// Roles, Producer and IndependenceWeight beyond the P1-CAP-04 set, that
// task_class stays the hard filter, that an unknown Role and an
// out-of-range IndependenceWeight are refused by identity and message,
// and that valid values round-trip.
func TestResourceRequestRankFields(t *testing.T) {
	checkRankFieldShape(t)
	valid := ResourceRequest{
		Intent: "rank", TaskClass: conductor.TaskClassCode,
		MinQuality: QualityStandard, Deadline: DeadlineInteractive,
		Roles:              []topology.Role{topology.RoleCritic, topology.RoleCoder},
		Producer:           &topology.ModelIdentity{CanonicalID: "m-1", Family: "f-1"},
		IndependenceWeight: new(0.5),
	}
	out, err := ValidateResourceRequest(valid, new(provider.SensitivityPublic))
	if err != nil {
		t.Fatalf("valid rank fields refused: %v", err)
	}
	if !reflect.DeepEqual(out.Roles, valid.Roles) || *out.Producer != *valid.Producer || *out.IndependenceWeight != 0.5 {
		t.Fatalf("rank fields did not round-trip: %+v", out)
	}
	for _, w := range []float64{0, 1} {
		r := valid
		r.IndependenceWeight = new(w)
		if _, err := ValidateResourceRequest(r, new(provider.SensitivityPublic)); err != nil {
			t.Fatalf("boundary weight %v refused: %v", w, err)
		}
	}
	hardFilter := valid
	hardFilter.TaskClass = "bogus"
	if _, err := ValidateResourceRequest(hardFilter, new(provider.SensitivityPublic)); err == nil {
		t.Fatal("unknown task_class accepted despite valid rank fields; task_class must stay the hard filter")
	}
	checkRankFieldRefusals(t, valid)
}

// checkRankFieldShape: ResourceRequest holds the P1-CAP-04 set plus exactly
// the three rank fields, with their contract types.
func checkRankFieldShape(t *testing.T) {
	t.Helper()
	base := []string{"Intent", "Scope", "TaskClass", "MinQuality", "Mutation", "Sensitivity",
		"RiskClass", "Deadline", "PreferredTier", "ReserveTier0", "DataClass"}
	want := map[string]reflect.Type{
		"Roles":              reflect.TypeFor[[]topology.Role](),
		"Producer":           reflect.TypeFor[*topology.ModelIdentity](),
		"IndependenceWeight": reflect.TypeFor[*float64](),
	}
	rt := reflect.TypeFor[ResourceRequest]()
	if rt.NumField() != len(base)+len(want) {
		t.Fatalf("ResourceRequest has %d fields, want %d", rt.NumField(), len(base)+len(want))
	}
	for _, n := range base {
		if _, ok := rt.FieldByName(n); !ok {
			t.Fatalf("P1-CAP-04 field %s missing", n)
		}
	}
	for n, typ := range want {
		f, ok := rt.FieldByName(n)
		if !ok || f.Type != typ {
			t.Fatalf("field %s = %v (present %v), want %v", n, f.Type, ok, typ)
		}
	}
}

// checkRankFieldRefusals: an unknown Role and an out-of-range
// IndependenceWeight are refused by sentinel identity, message and kind.
func checkRankFieldRefusals(t *testing.T, valid ResourceRequest) {
	t.Helper()
	const roleMsg = "invalid-input: capacity: unknown role in roles"
	const weightMsg = "invalid-input: capacity: independence_weight must lie in [0,1]"
	cases := []struct {
		name string
		mut  func(*ResourceRequest)
		want *cascade.Error
		msg  string
	}{
		{"unknown role", func(r *ResourceRequest) { r.Roles = []topology.Role{topology.RoleCoder, "bogus"} }, ErrUnknownRole, roleMsg},
		{"empty role", func(r *ResourceRequest) { r.Roles = []topology.Role{""} }, ErrUnknownRole, roleMsg},
		{"weight below 0", func(r *ResourceRequest) { r.IndependenceWeight = new(-0.01) }, ErrIndependenceWeightRange, weightMsg},
		{"weight above 1", func(r *ResourceRequest) { r.IndependenceWeight = new(1.01) }, ErrIndependenceWeightRange, weightMsg},
		{"weight NaN", func(r *ResourceRequest) { r.IndependenceWeight = new(math.NaN()) }, ErrIndependenceWeightRange, weightMsg},
	}
	for _, c := range cases {
		r := valid
		c.mut(&r)
		_, err := ValidateResourceRequest(r, new(provider.SensitivityPublic))
		ce, ok := err.(*cascade.Error)
		if !ok || ce != c.want || err.Error() != c.msg {
			t.Fatalf("%s: err = %v, want sentinel %q", c.name, err, c.msg)
		}
		if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput {
			t.Fatalf("%s: kind = %v, want KindInvalidInput", c.name, k)
		}
	}
}
