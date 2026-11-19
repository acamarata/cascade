// Package consult provides work class validation utilities.
// P2-JDG-75 classifier. Error code D3543.
package consult

import "testing"

// TestValidateCheckClass covers check and read-long classes.
func TestValidateCheckClass(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "check"},
		{name: "read-long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ValidateWorkClass(tt.name)
			if got.Class != tt.name {
				t.Errorf("Class = %q, want %q", got.Class, tt.name)
			}
			if !got.ReadOnly {
				t.Error("ReadOnly = false, want true")
			}
			if !got.RequiresVerify {
				t.Error("RequiresVerify = false, want true")
			}
		})
	}
}

// TestValidateExtractClass covers the extract class.
func TestValidateExtractClass(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "extract"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ValidateWorkClass(tt.name)
			if got.Class != tt.name {
				t.Errorf("Class = %q, want %q", got.Class, tt.name)
			}
			if !got.ReadOnly {
				t.Error("ReadOnly = false, want true")
			}
			if !got.RequiresVerify {
				t.Error("RequiresVerify = false, want true")
			}
		})
	}
}

// TestValidateArchitectClass covers architect, review, and review-deep classes.
func TestValidateArchitectClass(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "architect"},
		{name: "review"},
		{name: "review-deep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ValidateWorkClass(tt.name)
			if got.Class != tt.name {
				t.Errorf("Class = %q, want %q", got.Class, tt.name)
			}
			if !got.ReadOnly {
				t.Error("ReadOnly = false, want true")
			}
			if !got.RequiresVerify {
				t.Error("RequiresVerify = false, want true")
			}
		})
	}
}

// TestValidateGateClass covers the gate class.
func TestValidateGateClass(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "gate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ValidateWorkClass(tt.name)
			if got.Class != tt.name {
				t.Errorf("Class = %q, want %q", got.Class, tt.name)
			}
			if !got.ReadOnly {
				t.Error("ReadOnly = false, want true")
			}
			if !got.RequiresVerify {
				t.Error("RequiresVerify = false, want true")
			}
		})
	}
}
