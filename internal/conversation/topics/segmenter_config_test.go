// Package topics (segmenter_config_test.go): Purpose: HysteresisConfig.
// Validate's every branch, and sanity checks on the package-level
// constants classify() and NewSegmenter's stack construction depend on.
package topics

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestHysteresisConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     HysteresisConfig
		wantErr bool
	}{
		{"valid", HysteresisConfig{Threshold: 0.5, Window: 2}, false},
		{"valid window one", HysteresisConfig{Threshold: 0.05, Window: 1}, false},
		{"window zero", HysteresisConfig{Threshold: 0.5, Window: 0}, true},
		{"window negative", HysteresisConfig{Threshold: 0.5, Window: -1}, true},
		// Threshold 0 is refused rather than read as "maximally
		// sensitive": every pair of non-identical vectors is at distance
		// >0, so a 0 floor makes every transition a spike and disables the
		// distance signal it was meant to tune.
		{"threshold zero", HysteresisConfig{Threshold: 0, Window: 2}, true},
		{"threshold negative", HysteresisConfig{Threshold: -0.1, Window: 1}, true},
		{"threshold NaN", HysteresisConfig{Threshold: math.NaN(), Window: 1}, true},
		{"threshold +Inf", HysteresisConfig{Threshold: math.Inf(1), Window: 1}, true},
		{"threshold -Inf", HysteresisConfig{Threshold: math.Inf(-1), Window: 1}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.wantErr && !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("Validate(%+v) = %v, want KindInvalidInput", c.cfg, err)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("Validate(%+v) = %v, want nil", c.cfg, err)
			}
		})
	}
}

func TestTopicStackDefaultDepthIsPositive(t *testing.T) {
	if defaultTopicStackDepth < 1 {
		t.Fatalf("defaultTopicStackDepth = %d, want >= 1 (a non-positive depth disables the stack entirely)",
			defaultTopicStackDepth)
	}
}

func TestSegmenterClassifyPromptRendersTurnText(t *testing.T) {
	rendered := fmt.Sprintf(classifyPrompt, "hello world")
	if !strings.Contains(rendered, "hello world") {
		t.Fatalf("classifyPrompt rendering = %q, want it to contain the turn text", rendered)
	}
	if strings.Count(rendered, "%s") != 0 {
		t.Fatalf("classifyPrompt rendering = %q, want no unrendered format verbs", rendered)
	}
}
