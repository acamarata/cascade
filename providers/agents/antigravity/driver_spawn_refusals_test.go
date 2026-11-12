package antigravity_test

// Purpose: Spawn's refusals before and at process start.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	ag "github.com/acamarata/cascade/providers/agents/antigravity"
)

// newKitDriver builds a driver over the test binary with cfg overrides.
func newKitDriver(t *testing.T, home string, edit func(*ag.Config)) *ag.Driver {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := ag.Config{Binary: bin, RuntimeHome: home, Clock: fixedClock{time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)},
		Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}
	edit(&cfg)
	d, err := ag.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestAntigravitySpawnRefusals: no entitlement, no output filter, no clock,
// a canceled context and a worktree that does not exist each refuse.
func TestAntigravitySpawnRefusals(t *testing.T) {
	d, spec := kit(t)
	ctx := context.Background()
	home := spec.Env.Home
	noEntitlement := newKitDriver(t, home, func(c *ag.Config) { c.Posture = provider.CompliancePosture{} })
	if _, err := noEntitlement.Spawn(ctx, spec); !errors.Is(err, provider.ErrEntitlement) {
		t.Errorf("no entitlement = %v", err)
	}
	noFilter := spec
	noFilter.Filter = nil
	if _, err := d.Spawn(ctx, noFilter); !errors.Is(err, provider.ErrEgressSubstitutionFailed) {
		t.Errorf("no filter = %v", err)
	}
	noClock := newKitDriver(t, home, func(c *ag.Config) { c.Clock = nil })
	if _, err := noClock.Spawn(ctx, spec); !errors.Is(err, ag.ErrNoClock) {
		t.Errorf("no clock = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.Spawn(canceled, spec); !errors.Is(err, ag.ErrRunCancelled) {
		t.Errorf("canceled = %v", err)
	}
	noEnv := spec
	noEnv.Env.Driver = nil
	if _, err := d.Spawn(ctx, noEnv); !errors.Is(err, ag.ErrEmptySpawnEnv) {
		t.Errorf("empty env = %v", err)
	}
	expired := spec
	expired.Envelope.ExpiresAt = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	if _, err := d.Spawn(ctx, expired); err == nil {
		t.Error("expired envelope spawned")
	}
	missing := spec
	missing.WorktreeRoot = filepath.Join(spec.WorktreeRoot, "missing")
	missing.Envelope.Roots = []string{missing.WorktreeRoot}
	if _, err := d.Spawn(ctx, missing); err == nil {
		t.Error("spawn in a missing worktree succeeded")
	}
}

// TestAntigravityDefaultBinaryName: an empty Binary means "agy", reported by
// Executable even when the driver is refused.
func TestAntigravityDefaultBinaryName(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	d := newKitDriver(t, t.TempDir(), func(c *ag.Config) { c.Binary = "" })
	if d.Executable() != "agy" {
		t.Fatalf("Executable = %q, want agy", d.Executable())
	}
	if _, err := d.Capabilities(context.Background(), ""); !errors.Is(err, ag.ErrInterfaceAbsent) {
		t.Fatalf("Capabilities = %v, want ErrInterfaceAbsent", err)
	}
}

// TestAntigravityMalformedStreamFails: a child line that is not stream-json
// kills the group and the job ends failed with the decoder's error.
func TestAntigravityMalformedStreamFails(t *testing.T) {
	d, spec := kit(t)
	spec.Prompt = "garbage"
	ctx := context.Background()
	r, err := d.Spawn(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Collect(ctx, r.JobID); err == nil {
		t.Fatal("malformed stream collected without error")
	}
	if st, _ := d.Status(ctx, r.JobID); st != provider.AgentRunFailed {
		t.Fatalf("Status = %q, want failed", st)
	}
}
