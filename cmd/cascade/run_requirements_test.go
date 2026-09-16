package main

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): --require's two namespaces — that the closed lane
//   vocabulary still refuses a typo, and that the node namespace accepts
//   the open capability set without becoming a place typos go to die.
// SPORT: cmd/cascade tests (ADD) — P1-E17-W4-S37-T2.

// TestLaneRequirementsStillRefuseAnUnknownKey is the property that must
// NOT have been loosened by adding a second namespace. A typo in a lane
// key has to fail here, not reach a provider as nothing.
func TestLaneRequirementsStillRefuseAnUnknownKey(t *testing.T) {
	for _, key := range []string{"reasonning", "ctx", "browser", "structured_output"} {
		_, err := buildRequirements(map[string]string{key: "true"})
		if err == nil {
			t.Errorf("--require %s=true was accepted", key)
			continue
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Errorf("%s: kind = %v (ok=%v), want KindInvalidInput", key, kind, ok)
		}
	}
}

// TestLaneRequirementsStillParse keeps the check above honest: the three
// known keys must still work.
func TestLaneRequirementsStillParse(t *testing.T) {
	got, err := buildRequirements(map[string]string{
		"reasoning": "high", "context": "128000", "structured": "true",
	})
	if err != nil {
		t.Fatalf("buildRequirements: %v", err)
	}
	if got.Reasoning != "high" || got.Context != 128000 || !got.Structured {
		t.Fatalf("requirements = %+v", got)
	}
	if len(got.NodeCapabilities) != 0 {
		t.Errorf("lane-only requirements produced node capabilities %v", got.NodeCapabilities)
	}
}

// TestNodeCapabilitiesAreCollected is the new surface: an open set of
// machine-advertised capabilities, reachable through the same flag.
func TestNodeCapabilitiesAreCollected(t *testing.T) {
	got, err := buildRequirements(map[string]string{
		"node.browser": "true",
		"node.docker":  "true",
		"reasoning":    "high",
	})
	if err != nil {
		t.Fatalf("buildRequirements: %v", err)
	}
	if got.Reasoning != "high" {
		t.Errorf("the lane key was lost: %+v", got)
	}
	// Sorted, so the same flags always produce the same request — a set
	// whose order depended on map iteration would make two identical
	// invocations send different bytes.
	want := []string{"browser", "docker"}
	if len(got.NodeCapabilities) != len(want) {
		t.Fatalf("node capabilities = %v, want %v", got.NodeCapabilities, want)
	}
	for i := range want {
		if got.NodeCapabilities[i] != want[i] {
			t.Fatalf("node capabilities = %v, want %v (sorted)", got.NodeCapabilities, want)
		}
	}
}

// TestAnUnknownNodeCapabilityIsAccepted is the deliberate asymmetry, and
// the reason the namespace exists. Node capabilities are advertised by
// machines, so the CLI cannot hold a valid list; placement refuses an
// unmatched one later, by reporting no eligible node.
func TestAnUnknownNodeCapabilityIsAccepted(t *testing.T) {
	got, err := buildRequirements(map[string]string{"node.quantum-annealer": "true"})
	if err != nil {
		t.Fatalf("a capability this build has never heard of was refused at the CLI: %v", err)
	}
	if len(got.NodeCapabilities) != 1 || got.NodeCapabilities[0] != "quantum-annealer" {
		t.Fatalf("node capabilities = %v", got.NodeCapabilities)
	}
}

// TestRequiringACapabilityToBeAbsentIsRefused proves the false case is a
// refusal rather than a silent no-op. Silently ignoring it would leave a
// user believing they had constrained placement when they had not.
func TestRequiringACapabilityToBeAbsentIsRefused(t *testing.T) {
	_, err := buildRequirements(map[string]string{"node.browser": "false"})
	if err == nil {
		t.Fatal("--require node.browser=false was accepted")
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Errorf("error = %v, want it to explain why the false case is meaningless", err)
	}
}

// TestMalformedNodeRequirementsAreRefused covers the remaining guards.
func TestMalformedNodeRequirementsAreRefused(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"no capability after the prefix", "node.", "true"},
		{"blank capability", "node.   ", "true"},
		{"a non-boolean value", "node.browser", "yes-please"},
		{"an empty value", "node.browser", ""},
	} {
		if _, err := buildRequirements(map[string]string{tc.key: tc.value}); err == nil {
			t.Errorf("%s: --require %s=%s was accepted", tc.name, tc.key, tc.value)
		}
	}
}

// TestNoRequirementsIsNotAnError proves the flag stays optional.
func TestNoRequirementsIsNotAnError(t *testing.T) {
	got, err := buildRequirements(nil)
	if err != nil {
		t.Fatalf("buildRequirements(nil): %v", err)
	}
	if got.NodeCapabilities != nil || got.Reasoning != "" {
		t.Fatalf("requirements = %+v, want the zero value", got)
	}
}
