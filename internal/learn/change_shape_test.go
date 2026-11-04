package learn

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestValidateChangeShapeCases(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		kind         ProposedChangeKind
		change       Change
		loosens      bool
		errKind      cascade.Kind
	}{
		{"add", "ci.local_steps", "ci", Change{OpAddStep, "ci.local.build", `"cascade context generate --check"`}, false, 0},
		{"alias", "ci.steps", "ci", Change{OpAddStep, "ci.local.build", `"cascade context generate --check"`}, false, 0},
		{"reorder", "conductor.lane_order", "workflow", Change{OpReorder, "conductor.lane_order", `["l2","l1"]`}, false, 0},
		{"parameterise", "ci.local_timeout", "rule", Change{OpSet, "ci.local.timeout_seconds", "120"}, false, 0},
		{"minimum", "ci.local_timeout", "policy", Change{OpSet, "ci.local.timeout_seconds", "60"}, false, 0},
		{"static default", "ci.local_timeout", "ci", Change{OpSet, "ci.local.timeout_seconds", "300"}, false, 0},
		{"above bound", "ci.local_timeout", "ci", Change{OpSet, "ci.local.timeout_seconds", "301"}, true, cascade.KindPolicyDenied},
		{"below bound", "ci.local_timeout", "ci", Change{OpSet, "ci.local.timeout_seconds", "59"}, true, cascade.KindPolicyDenied},
		{"remove", "ci.local_steps", "ci", Change{"remove", "ci.local.build", ""}, false, cascade.KindPolicyDenied},
		{"delete", "ci.local_steps", "ci", Change{"delete", "ci.local.build", ""}, false, cascade.KindPolicyDenied},
		{"unknown op", "ci.local_steps", "ci", Change{"other", "ci.local.build", ""}, false, cascade.KindPolicyDenied},
		{"wrong shape", "ci.local_steps", "ci", Change{OpSet, "ci.local.build", "1"}, true, cascade.KindPolicyDenied},
		{"unknown step", "ci.local_steps", "ci", Change{OpAddStep, "ci.local.build", `"other"`}, true, cascade.KindPolicyDenied},
		{"denied alias", "auth_rules", "policy", Change{OpSet, "policy.auth.check", "1"}, false, cascade.KindPolicyDenied},
		{"invalid number", "ci.local_timeout", "ci", Change{OpSet, "ci.local.timeout_seconds", "NaN"}, false, cascade.KindInvalidInput},
		{"outside path", "ci.local_timeout", "ci", Change{OpSet, "retrieval.fusion.k", "120"}, false, cascade.KindInvalidInput},
		{"unknown target", "unknown.target", "ci", Change{OpSet, "unknown.target", "1"}, false, cascade.KindNotFound},
		{"unknown kind", "ci.local_timeout", "unknown", Change{OpSet, "ci.local.timeout_seconds", "120"}, false, cascade.KindInvalidInput},
		{"empty kind", "ci.local_timeout", "", Change{OpSet, "ci.local.timeout_seconds", "120"}, false, cascade.KindInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateChangeShape(tc.kind, tc.target, tc.change)
			if got.LoosensBound != tc.loosens {
				t.Errorf("loosens = %v, want %v", got.LoosensBound, tc.loosens)
			}
			if tc.errKind == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else if !cascade.HasKind(err, tc.errKind) {
				t.Fatalf("error = %v, want %s", err, tc.errKind)
			}
		})
	}
}
