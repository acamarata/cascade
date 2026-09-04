package secrets

// Purpose: the [secrets] config keys - defaults, bounds, and the parse
//
//	path a hot reload runs through. The rule under test is that an
//	unusable value is REFUSED, never clamped and never silently replaced
//	by the default: a security setting that lies about what it is doing
//	is worse than one that fails loudly.

import (
	"math"
	"testing"
)

// TestDefaultDetectionConfigMatchesTheRatifiedValues pins the two 08 §3
// [secrets] rows (R-16.47) so a drifting default is a failing test.
func TestDefaultDetectionConfigMatchesTheRatifiedValues(t *testing.T) {
	cfg := DefaultDetectionConfig()
	if cfg.EntropyFloor != 3.5 {
		t.Errorf("entropy_floor default = %v, want 3.5", cfg.EntropyFloor)
	}
	if cfg.ConfidenceThreshold != 0.8 {
		t.Errorf("confidence_threshold default = %v, want 0.8", cfg.ConfidenceThreshold)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the ratified defaults do not validate: %v", err)
	}
}

// TestDefaultThresholdSitsBetweenTheRungs is the precision-first
// invariant expressed as arithmetic: a structured or shape-only hit falls
// below the default threshold, a corroborated one clears it. Changing a
// rung without changing the default breaks this.
func TestDefaultThresholdSitsBetweenTheRungs(t *testing.T) {
	threshold := Confidence(DefaultConfidenceThreshold)
	if !(ConfidenceWeak < threshold && ConfidenceStructured < threshold) {
		t.Fatalf("a shape-only rung reaches the default threshold %v", threshold)
	}
	if !(ConfidenceCorroborated >= threshold && ConfidenceCertain >= threshold && ConfidenceProven >= threshold) {
		t.Fatalf("a corroborated rung does not reach the default threshold %v", threshold)
	}
}

// TestValidateRejectsOutOfRange covers both bounds in both directions.
func TestValidateRejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		cfg  DetectionConfig
	}{
		{"negative floor", DetectionConfig{EntropyFloor: -0.1, ConfidenceThreshold: 0.8}},
		{"floor above the byte maximum", DetectionConfig{EntropyFloor: 8.1, ConfidenceThreshold: 0.8}},
		{"NaN floor", DetectionConfig{EntropyFloor: math.NaN(), ConfidenceThreshold: 0.8}},
		{"zero threshold", DetectionConfig{EntropyFloor: 3.5}},
		{"negative threshold", DetectionConfig{EntropyFloor: 3.5, ConfidenceThreshold: -1}},
		{"threshold above one", DetectionConfig{EntropyFloor: 3.5, ConfidenceThreshold: 1.1}},
		{"NaN threshold", DetectionConfig{EntropyFloor: 3.5, ConfidenceThreshold: math.NaN()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatalf("%+v was accepted", tc.cfg)
			}
		})
	}
}

// TestValidateAcceptsTheEdges: the bounds themselves are legal values.
func TestValidateAcceptsTheEdges(t *testing.T) {
	for _, cfg := range []DetectionConfig{
		{EntropyFloor: 0, ConfidenceThreshold: 1},
		{EntropyFloor: 8, ConfidenceThreshold: 0.0001},
	} {
		if err := cfg.Validate(); err != nil {
			t.Errorf("%+v was refused: %v", cfg, err)
		}
	}
}

// TestParseDetectionConfig covers the reload path: absent keys default,
// numeric keys in either TOML shape are read, and a non-numeric or
// out-of-range key is refused rather than defaulted past.
func TestParseDetectionConfig(t *testing.T) {
	cfg, err := ParseDetectionConfig(map[string]interface{}{})
	if err != nil || cfg != DefaultDetectionConfig() {
		t.Fatalf("an empty section gave %+v (%v), want the defaults", cfg, err)
	}
	cfg, err = ParseDetectionConfig(map[string]interface{}{
		"entropy_floor": 4.25, "confidence_threshold": int64(1),
	})
	if err != nil {
		t.Fatalf("a valid section was refused: %v", err)
	}
	if cfg.EntropyFloor != 4.25 || cfg.ConfidenceThreshold != 1 {
		t.Fatalf("parsed %+v", cfg)
	}
	if _, err := ParseDetectionConfig(map[string]interface{}{"entropy_floor": 4}); err != nil {
		t.Fatalf("a bare int entropy_floor was refused: %v", err)
	}
	for name, section := range map[string]map[string]interface{}{
		"string floor":         {"entropy_floor": "3.5"},
		"bool threshold":       {"confidence_threshold": true},
		"out-of-range floor":   {"entropy_floor": 99.0},
		"out-of-range cutoff":  {"confidence_threshold": 2.0},
		"table-valued setting": {"entropy_floor": map[string]interface{}{}},
	} {
		if _, err := ParseDetectionConfig(section); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestConfigIsHotSwappedUnderConcurrentScans proves the reload seam is
// race-free: Reload runs while Scan runs, and -race must stay clean.
func TestConfigIsHotSwappedUnderConcurrentScans(t *testing.T) {
	d := testDetector(t)
	content := readCorpus(t, "api-key.txt")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			d.Scan(content)
		}
	}()
	for i := 0; i < 200; i++ {
		floor := 3.5
		if i%2 == 0 {
			floor = 4.5
		}
		if err := d.Reload(DetectionConfig{EntropyFloor: floor, ConfidenceThreshold: 0.8}); err != nil {
			t.Errorf("reload %d: %v", i, err)
		}
	}
	<-done
}
