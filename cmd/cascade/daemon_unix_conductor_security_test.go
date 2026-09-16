//go:build !windows

package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the security pipeline the conductor.execute door
//   will not open without. The W-3 hardening gate found this wiring absent
//   and every real dispatch refused at cascade's own door, so what these
//   tests hold is that the collaborators are REAL and COMPLETE — a partial
//   set restores exactly that failure.
// Constraints: construction only. Nothing here reads or writes a secret,
//   so no test reaches the operator's keychain.
// SPORT: cmd/cascade/daemon:conductor-security tests (ADD) — P1-E10-W4-S87-T1.

// TestTheSecurityPipelineIsBuiltComplete is the defect the W-3 gate found,
// asserted directly: conductor.Pipeline.Ready refuses if ANY collaborator
// is missing, and it refuses with a message naming none of them, so the
// useful assertion is per field rather than on a single verdict.
//
// The walk is by reflection rather than a hand-written list because the set
// is the point: a field added to ConductorSecurity and not wired here is
// exactly the W-3 defect returning, and a hand-written list would not see
// it. Two of today's fields (Taxonomy, Sensitivity) are struct values, so
// they are structurally non-nil and cannot fail this walk as wired now —
// the walk holds them against being changed to a nil pointer or interface,
// which is the form the defect took the first time.
func TestTheSecurityPipelineIsBuiltComplete(t *testing.T) {
	sec, err := conductorSecurity(grantPaths{dir: t.TempDir()})
	if err != nil {
		t.Fatalf("conductorSecurity: %v", err)
	}
	v := reflect.ValueOf(sec)
	if v.NumField() == 0 {
		t.Fatal("ConductorSecurity has no fields; this test would assert nothing")
	}
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if !nilableKinds[field.Kind()] {
			continue // a concrete value cannot be nil; there is nothing to assert
		}
		if field.IsNil() {
			t.Errorf("%s is nil; conductor.execute would refuse every call",
				v.Type().Field(i).Name)
		}
	}
}

// nilableKinds are the reflect kinds whose zero value is nil, so IsNil is
// defined on them. A map rather than a switch: the linter wall requires an
// exhaustive switch over reflect.Kind, which would be twenty-odd cases
// restating "not nilable".
var nilableKinds = map[reflect.Kind]bool{
	reflect.Interface: true, reflect.Pointer: true, reflect.Map: true,
	reflect.Slice: true, reflect.Func: true, reflect.Chan: true,
	reflect.UnsafePointer: true,
}

// TestTheSecurityPipelineRefusesWithNoDataDirectory holds the rule the
// file's own doc comment states: a failure to build is propagated, never
// swallowed into a partial struct. A partial struct would be returned
// alongside a nil error and the daemon would start with a door that
// refuses everything and explains nothing.
func TestTheSecurityPipelineRefusesWithNoDataDirectory(t *testing.T) {
	sec, err := conductorSecurity(nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if sec.Classifier != nil || sec.Firewall != nil || sec.Policy != nil {
		t.Error("a refused build still returned collaborators")
	}
	if _, err := daemonEgressFirewall(nil, nil); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("daemonEgressFirewall(nil): err = %v, want KindUnavailable", err)
	}
	if _, err := daemonCredentialSource(nil); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("daemonCredentialSource(nil): err = %v, want KindUnavailable", err)
	}
}

// TestTheScannerReportsCertainHitsOnly is the adapter's whole contract. A
// scanner that reported ambiguous signals would refuse prompts on a guess,
// and an operator who is refused on a guess learns to ignore the refusal —
// which costs more than not scanning at all.
func TestTheScannerReportsCertainHitsOnly(t *testing.T) {
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	scanner := detectorScanner{detector: detector}

	// Clean prose carries nothing, and must report nothing rather than an
	// empty-but-non-nil slice a caller could read as "scanned, found a hit
	// with no class".
	if got := scanner.ScanCertainClasses("please summarise the release notes"); got != nil {
		t.Errorf("clean content reported %v, want nil", got)
	}

	// A structurally certain credential must be reported, and reported by
	// CLASS rather than by value: the class is what the refusal message can
	// safely name.
	const secret = "AKIAIOSFODNN7EXAMPLE"
	got := scanner.ScanCertainClasses("deploy with " + secret)
	if len(got) == 0 {
		t.Fatalf("a certain credential reported no class; the scanner is not wired to the detector")
	}
	for _, class := range got {
		if class == "" {
			t.Error("a hit reported an empty class name")
		}
		if strings.Contains(class, secret) {
			t.Errorf("the class name %q carries the credential itself", class)
		}
	}
}
