package doctor

// Purpose: retrieval_fusion_default's true/false gate branches, driven
//   through the real CheckRegistry + Runner (this package's actual
//   production entry point for any Check — see registry.go's own doc
//   comment: no `cascade doctor` composition root exists in the P1 tree
//   yet, matching selfcheck.go/census.go/mcpcheck.go's identical
//   situation), plus the wiring-removed failure proof.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7 — no clock reads (fixed clock), no network.
// SPORT: placeholder: doctor/framework (ADD, P1-E06-W2-S12-T6).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
)

// fixedDoctorTestTime is the injected instant every test in this file
// sees (Art.7.3 — no wall clock in a test).
var fixedDoctorTestTime = time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)

// fakeFusionProvider is a minimal FusionEnabledProvider for the branch
// tests below.
type fakeFusionProvider bool

func (f fakeFusionProvider) RetrievalFusionEnabled() bool { return bool(f) }

func TestRetrievalFusionGateCheck_TrueBranch(t *testing.T) {
	check := NewRetrievalFusionGateCheck(fakeFusionProvider(true))
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusOK {
		t.Fatalf("enabled=true: Status = %v, want StatusOK", result.Status)
	}
}

func TestRetrievalFusionGateCheck_FalseBranch(t *testing.T) {
	check := NewRetrievalFusionGateCheck(fakeFusionProvider(false))
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusWarn {
		t.Fatalf("enabled=false: Status = %v, want StatusWarn", result.Status)
	}
	if result.Remediation == "" {
		t.Error("enabled=false: want a non-empty Remediation")
	}
}

func TestRetrievalFusionGateCheck_NilConfig(t *testing.T) {
	check := &retrievalFusionCheck{config: nil}
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != StatusError {
		t.Fatalf("nil config: Status = %v, want StatusError (Art.1 — never a silent ok)", result.Status)
	}
}

func TestRetrievalFusionGateCheck_NotFixable(t *testing.T) {
	check := NewRetrievalFusionGateCheck(fakeFusionProvider(false))
	if check.Metadata().Fixable {
		t.Fatal("retrieval_fusion_default declares Fixable=true but has no remediation logic")
	}
	if _, err := check.Fix(context.Background()); err != ErrCheckNotFixable {
		t.Fatalf("Fix() error = %v, want ErrCheckNotFixable", err)
	}
}

// configAdapter is the composition-root-shaped adapter a real caller
// wires *runtime.Config through: Config.FusionEnabled is a field, not a
// method, so satisfying FusionEnabledProvider takes one line at the call
// site, exactly like every other Check in this package that is
// constructed over an injected interface rather than a concrete type.
type configAdapter struct{ cfg *cascaderuntime.Config }

func (a configAdapter) RetrievalFusionEnabled() bool { return a.cfg.FusionEnabled }

// TestRetrievalFusionGateNote drives the check through the real
// CheckRegistry and Runner — this package's real entry point — with the
// real *runtime.Config the real Load produces from an explicit
// `enabled = false` override, and asserts the R-16.9 note actually
// reaches the RunReport. It then proves the wiring can fail: an empty
// registry produces no such entry at all.
func TestRetrievalFusionGateNote(t *testing.T) {
	cfg, err := cascaderuntime.Load(context.Background(), cascaderuntime.LoadOptions{
		Path:    writeFusionDisabledConfig(t),
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	})
	if err != nil {
		t.Fatalf("runtime.Load: %v", err)
	}
	if cfg.FusionEnabled {
		t.Fatal("test config did not actually disable fusion; the rest of this test would be vacuous")
	}

	reg := NewCheckRegistry()
	reg.Register(NewRetrievalFusionGateCheck(configAdapter{cfg: cfg}))
	report := Run(context.Background(), reg.List(), RunOptions{
		Clock: cascaderuntime.NewFixedClock(fixedDoctorTestTime),
	})

	entry := findEntry(t, report, "retrieval_fusion_default")
	if entry.Result.Status != StatusWarn {
		t.Fatalf("real Config with fusion disabled: Status = %v, want StatusWarn", entry.Result.Status)
	}

	// Prove the wiring itself is load-bearing: an empty registry — the
	// check never registered — produces no entry at all, which is the
	// failure this test would show if retrieval_fusion_default were
	// silently dropped from composition.
	emptyReport := Run(context.Background(), NewCheckRegistry().List(), RunOptions{
		Clock: cascaderuntime.NewFixedClock(fixedDoctorTestTime),
	})
	for _, e := range emptyReport.Entries {
		if e.Name == "retrieval_fusion_default" {
			t.Fatal("unregistered check unexpectedly present in the report")
		}
	}
}

// writeFusionDisabledConfig writes a real config.toml explicitly setting
// retrieval.fusion.enabled = false.
func writeFusionDisabledConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[retrieval.fusion]\nenabled = false\n"), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return path
}

// findEntry locates name in report, failing the test if absent.
func findEntry(t *testing.T, report RunReport, name string) ReportEntry {
	t.Helper()
	for _, e := range report.Entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no %q entry in report: %+v", name, report.Entries)
	return ReportEntry{}
}
