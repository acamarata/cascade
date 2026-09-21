package doctor

// Purpose: registry_pubkey's three branches (unconfigured / configured
//   with a key / configured without one), driven through Run directly and
//   through the real CheckRegistry + Runner — this package's actual
//   production entry point for any Check, per retrieval_fusion_test.go's
//   own identical reasoning (no `cascade doctor` composition root exists
//   in this package's own tree; cmd/cascade/doctor_mounts.go mounts it).
// Constraints: Art.7 — no wall clock, no network.
// SPORT: doctor/registry-pubkey (ADD, P1-E24-W5-S50-T2).

import (
	"context"
	"testing"
	"time"
)

// fakeRegistryPubkeyProvider is a minimal RegistryPubkeyProvider for the
// branch tests below.
type fakeRegistryPubkeyProvider struct {
	url, pubkeyPath string
}

func (f fakeRegistryPubkeyProvider) RegistryURL() string        { return f.url }
func (f fakeRegistryPubkeyProvider) RegistryPubkeyPath() string { return f.pubkeyPath }

func TestRegistryPubkeyCheck_Unconfigured(t *testing.T) {
	check := NewRegistryPubkeyCheck(fakeRegistryPubkeyProvider{})
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusOK {
		t.Fatalf("no url: Status = %v, want StatusOK", result.Status)
	}
}

func TestRegistryPubkeyCheck_ConfiguredWithKey(t *testing.T) {
	check := NewRegistryPubkeyCheck(fakeRegistryPubkeyProvider{url: "https://registry.example", pubkeyPath: "/etc/cascade/registry.pub"})
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusOK {
		t.Fatalf("url+pubkey: Status = %v, want StatusOK", result.Status)
	}
}

// TestRegistryPubkeyCheck_AbsentPubkey is D1's own failing input: a
// [registry].url configured with no pubkey_path — the fail-closed state
// R-14.75 requires. This is the concrete case a real operator hits until
// the owner action item (a real registry signing key) is filled in.
func TestRegistryPubkeyCheck_AbsentPubkey(t *testing.T) {
	check := NewRegistryPubkeyCheck(fakeRegistryPubkeyProvider{url: "https://registry.example"})
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusWarn {
		t.Fatalf("url, no pubkey: Status = %v, want StatusWarn", result.Status)
	}
	if result.Remediation == "" {
		t.Error("url, no pubkey: want a non-empty Remediation")
	}
}

func TestRegistryPubkeyCheck_NilConfig(t *testing.T) {
	check := &registryPubkeyCheck{config: nil}
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusError {
		t.Fatalf("nil config: Status = %v, want StatusError (Art.1 — never a silent ok)", result.Status)
	}
}

func TestRegistryPubkeyCheck_NotFixable(t *testing.T) {
	check := NewRegistryPubkeyCheck(fakeRegistryPubkeyProvider{})
	if check.Metadata().Fixable {
		t.Fatal("registry_pubkey declares Fixable=true but has no remediation logic")
	}
	if _, err := check.Fix(context.Background()); err != ErrCheckNotFixable {
		t.Fatalf("Fix() error = %v, want ErrCheckNotFixable", err)
	}
}

// TestRegistryPubkeyCheck_ViaRealRegistry proves the check is a real,
// registrable Check — the same CheckRegistry.Register + Run(registry.
// List(), ...) path cmd/cascade/doctor_mounts.go's productionCheckRegistry
// and doctor.go's run dispatch use in production — not merely a struct
// satisfying the interface in isolation.
func TestRegistryPubkeyCheck_ViaRealRegistry(t *testing.T) {
	reg := NewCheckRegistry()
	reg.Register(NewRegistryPubkeyCheck(fakeRegistryPubkeyProvider{url: "https://registry.example"}))
	report := Run(context.Background(), reg.List(), RunOptions{Clock: fixedRegistryPubkeyClock{}})
	found := false
	for _, e := range report.Entries {
		if e.Name == "registry_pubkey" {
			found = true
			if e.Result.Status != StatusWarn {
				t.Errorf("registry_pubkey via real registry: Status = %v, want StatusWarn", e.Result.Status)
			}
		}
	}
	if !found {
		t.Fatal("registry_pubkey did not appear in the real Run report")
	}
}

// fixedRegistryPubkeyClock is Art.7.3's required injected clock (no bare
// time.Now in a test).
type fixedRegistryPubkeyClock struct{}

func (fixedRegistryPubkeyClock) Now() time.Time { return fixedDoctorTestTime }
