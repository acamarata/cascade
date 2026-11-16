package fence

import (
	"testing"
)

// TestTypesCompile verifies that the core types and constants are properly defined
// and have expected runtime values.
func TestTypesCompile(t *testing.T) {
	// Assert marker delimiter constants have expected values
	if MarkerOpen != "[[" {
		t.Errorf("MarkerOpen = %q, want %q", MarkerOpen, "[[")
	}
	if MarkerClose != "]]" {
		t.Errorf("MarkerClose = %q, want %q", MarkerClose, "]]")
	}

	// Verify FenceResult can be instantiated
	result := FenceResult{
		Clean:      true,
		Confidence: 1.0,
		Markers:    []MarkerPair{},
	}
	if !result.Clean {
		t.Error("expected Clean to be true")
	}

	// Verify MarkerPair can be instantiated
	pair := MarkerPair{
		OpenPos:  0,
		ClosePos: 2,
	}
	if pair.OpenPos != 0 || pair.ClosePos != 2 {
		t.Errorf("unexpected MarkerPair values: %+v", pair)
	}

	// Verify ProbeFunc type exists and can be assigned
	var probe ProbeFunc = func(data string) FenceResult {
		return FenceResult{Clean: true}
	}
	_ = probe
}
