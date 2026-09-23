package ci

import "testing"

// TestCIRequirement_ZeroValueAllRequired proves 06 §5 rule 20's
// fail-closed rule: an all-false zero CIRequirement is treated as
// AllRequired by Requires, for every one of the seven kinds.
func TestCIRequirement_ZeroValueAllRequired(t *testing.T) {
	var zero CIRequirement
	for _, kind := range allRequirementKinds {
		if !zero.Requires(kind) {
			t.Errorf("zero CIRequirement.Requires(%s) = false, want true (fail-closed)", kind)
		}
	}
}

// TestCIRequirement_ExplicitValueRespected proves a non-zero
// CIRequirement is read field-by-field, not collapsed to AllRequired.
func TestCIRequirement_ExplicitValueRespected(t *testing.T) {
	r := CIRequirement{Lint: true, Unit: true}
	cases := map[RequirementKind]bool{
		RequirementFormat:       false,
		RequirementLint:         true,
		RequirementCompile:      false,
		RequirementUnit:         true,
		RequirementIntegration:  false,
		RequirementArchitecture: false,
		RequirementSecurity:     false,
	}
	for kind, want := range cases {
		if got := r.Requires(kind); got != want {
			t.Errorf("Requires(%s) = %v, want %v", kind, got, want)
		}
	}
}

// TestCIRequirement_AllRequiredSentinel proves the AllRequired var
// itself requires every kind, and that a caller-invalid kind is
// fail-closed to required=true.
func TestCIRequirement_AllRequiredSentinel(t *testing.T) {
	for _, kind := range allRequirementKinds {
		if !AllRequired.Requires(kind) {
			t.Errorf("AllRequired.Requires(%s) = false, want true", kind)
		}
	}
	if !AllRequired.Requires(RequirementKind("bogus")) {
		t.Error("Requires on an invalid kind = false, want true (fail-closed)")
	}
}

// TestRequirementKind_Valid proves the closed seven-member enum's Valid
// method accepts exactly its own members and rejects the zero value and
// an out-of-vocabulary string.
func TestRequirementKind_Valid(t *testing.T) {
	for _, kind := range allRequirementKinds {
		if !kind.Valid() {
			t.Errorf("%s.Valid() = false, want true", kind)
		}
	}
	if RequirementKind("").Valid() {
		t.Error(`RequirementKind("").Valid() = true, want false`)
	}
	if RequirementKind("bogus").Valid() {
		t.Error(`RequirementKind("bogus").Valid() = true, want false`)
	}
	if len(allRequirementKinds) != 7 {
		t.Fatalf("allRequirementKinds has %d members, want exactly 7", len(allRequirementKinds))
	}
}
