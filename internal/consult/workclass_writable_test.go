package consult

import "testing"

// TestValidateBuildClass verifies that writable work classes return the correct constraints.
func TestValidateBuildClass(t *testing.T) {
	classes := []string{"write", "build-simple", "build", "build-hard"}
	for _, name := range classes {
		t.Run(name, func(t *testing.T) {
			cc, err := ValidateWorkClass(name)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", name, err)
			}
			if cc.Class != name {
				t.Errorf("Class = %q, want %q", cc.Class, name)
			}
			if cc.ReadOnly != false {
				t.Errorf("ReadOnly = %v, want false", cc.ReadOnly)
			}
			if cc.RequiresVerify != true {
				t.Errorf("RequiresVerify = %v, want true", cc.RequiresVerify)
			}
		})
	}
}
