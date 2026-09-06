package provider_test

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestNewCompliancePostureAlwaysForbidsCredentialSharing(t *testing.T) {
	posture := provider.NewCompliancePosture(
		[]string{"api-key", "oauth"},
		true,
		true,
		[]string{"batch"},
		"steady",
		true,
	)
	if posture.CredentialSharing != provider.CredentialSharingForbidden {
		t.Fatalf("CredentialSharing = %q, want %q", posture.CredentialSharing, provider.CredentialSharingForbidden)
	}
	if err := posture.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestNewCompliancePostureFieldsRoundTrip(t *testing.T) {
	posture := provider.NewCompliancePosture(
		[]string{"api-key"},
		false,
		true,
		[]string{"scheduled"},
		"burst-then-cooldown",
		false,
	)
	if len(posture.AuthModes) != 1 || posture.AuthModes[0] != "api-key" {
		t.Fatalf("AuthModes = %v, want [api-key]", posture.AuthModes)
	}
	if posture.InteractiveEntitlement {
		t.Fatal("InteractiveEntitlement should be false")
	}
	if !posture.ProgrammaticEntitlement {
		t.Fatal("ProgrammaticEntitlement should be true")
	}
	if posture.Pacing != "burst-then-cooldown" {
		t.Fatalf("Pacing = %q, want burst-then-cooldown", posture.Pacing)
	}
	if posture.MultiProfileEnabled {
		t.Fatal("MultiProfileEnabled should be false")
	}
}

// TestCompliancePostureValidateFailsClosed asserts a hand-built
// CompliancePosture (bypassing NewCompliancePosture) that carries a
// non-forbidden CredentialSharing value is rejected, not silently accepted.
func TestCompliancePostureValidateFailsClosed(t *testing.T) {
	posture := provider.CompliancePosture{
		CredentialSharing: provider.CredentialSharingPolicy("allowed"),
	}
	err := posture.Validate()
	if err == nil {
		t.Fatal("Validate() should refuse a non-forbidden credential_sharing value")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Validate() kind = %v, %v, want KindInvalidInput, true", kind, ok)
	}
}

func TestCompliancePostureValidateAcceptsZeroValueWithForbiddenSet(t *testing.T) {
	posture := provider.CompliancePosture{CredentialSharing: provider.CredentialSharingForbidden}
	if err := posture.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestCompliancePostureValidateRefusesEmptyCredentialSharing(t *testing.T) {
	var posture provider.CompliancePosture
	if err := posture.Validate(); err == nil {
		t.Fatal("Validate() should refuse the zero-value (empty) credential_sharing")
	}
}
