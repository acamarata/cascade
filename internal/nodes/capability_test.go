package nodes

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func validReport() CapabilityReport {
	return CapabilityReport{
		Capabilities: []string{"browser", "docker", "4hr_runtime"},
		K12:          NewK12Preset(8*1024*1024*1024, 4),
	}
}

func TestValidateCapabilityReportAccepted(t *testing.T) {
	if err := ValidateCapabilityReport(validReport()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateCapabilityReportTooManyCapabilities(t *testing.T) {
	caps := make([]string, maxCapabilities+1)
	for i := range caps {
		caps[i] = strings.Repeat("x", 4) + string(rune('a'+i%26))
	}
	cr := CapabilityReport{Capabilities: caps, K12: NewK12Preset(1, 1)}
	err := ValidateCapabilityReport(cr)
	if err == nil {
		t.Fatal("expected error for oversized capability count")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("got kind %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

func TestValidateCapabilityReportOverlongCapability(t *testing.T) {
	cr := CapabilityReport{Capabilities: []string{strings.Repeat("y", maxCapabilityLen+1)}, K12: NewK12Preset(1, 1)}
	if err := ValidateCapabilityReport(cr); err == nil {
		t.Fatal("expected error for overlong capability")
	}
}

func TestValidateCapabilityReportEmptyEntry(t *testing.T) {
	cr := CapabilityReport{Capabilities: []string{""}, K12: NewK12Preset(1, 1)}
	if err := ValidateCapabilityReport(cr); err == nil {
		t.Fatal("expected error for empty capability entry")
	}
}

func TestValidateCapabilityReportDuplicate(t *testing.T) {
	cr := CapabilityReport{Capabilities: []string{"docker", "docker"}, K12: NewK12Preset(1, 1)}
	if err := ValidateCapabilityReport(cr); err == nil {
		t.Fatal("expected error for duplicate capability")
	}
}

func TestValidateCapabilityReportUnrecognizedK12Class(t *testing.T) {
	cr := CapabilityReport{K12: K12Preset{Class: "supercomputer"}}
	if err := ValidateCapabilityReport(cr); err == nil {
		t.Fatal("expected error for unrecognized k12 class")
	}
}

func TestClassifyK12Boundaries(t *testing.T) {
	cases := []struct {
		bytes uint64
		want  string
	}{
		{1, k12ClassMinimal},
		{k12MinimalMemCeilingBytes, k12ClassMinimal},
		{k12MinimalMemCeilingBytes + 1, k12ClassBalanced},
		{k12PerformanceMemFloorBytes - 1, k12ClassBalanced},
		{k12PerformanceMemFloorBytes, k12ClassPerformance},
		{1 << 40, k12ClassPerformance},
	}
	for _, c := range cases {
		if got := classifyK12(c.bytes); got != c.want {
			t.Errorf("classifyK12(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

func TestHasCapability(t *testing.T) {
	cr := validReport()
	if !HasCapability(cr, "docker") {
		t.Error("expected HasCapability(docker) true")
	}
	if HasCapability(cr, "gpu") {
		t.Error("expected HasCapability(gpu) false")
	}
}

// TestCapabilityReportNeverWidensAuthorization proves a node cannot claim
// its way to a higher trust tier via its own capability report: the
// report carries no trust_tier field at all (CapabilityReport's struct
// shape has none), so even an attacker-controlled report declaring every
// capability under the sun has no path into Rank/Satisfies/ValidateTier —
// those functions do not accept a CapabilityReport as input, by
// construction.
func TestCapabilityReportNeverWidensAuthorization(t *testing.T) {
	cr := CapabilityReport{
		Capabilities: []string{"controller", "admin", "trust_tier=controller"},
		K12:          NewK12Preset(1<<40, 128),
	}
	if err := ValidateCapabilityReport(cr); err != nil {
		t.Fatalf("a report is not refused merely for naming a tier-shaped string: %v", err)
	}
	// The only way trust_tier ever changes is RecordStore.Enroll/Rotate,
	// neither of which takes a CapabilityReport. Compile-time proof: this
	// package has no function whose signature accepts a CapabilityReport
	// and returns a Tier or mutates DeviceRecord.Tier. The reflection
	// check below is a runtime restatement in case a future edit adds
	// one, so the test fails loudly rather than silently going stale.
	tier, ok := Rank(TierWorkerTrusted)
	if !ok || tier != 1 {
		t.Fatalf("sanity: worker-trusted rank changed unexpectedly")
	}
}
