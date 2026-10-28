//go:build !windows

// Purpose: the CLI launch environment's pieces: the egress registrar
// adapter treats a repeat registration as success, and the recorder lock is
// exclusive across open files.
// SPORT: cmd/cascade plugin launch environment tests (P1-PLG-03).
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins/process"
)

func TestPluginEgressRegistrarIsIdempotent(t *testing.T) {
	reg := pluginEgressRegistrar{reg: egress.NewRegistry()}
	cfg := process.EgressRegistrationConfig{Enabled: true, AllowedTiers: []process.SensitivityTier{process.TierPublic}, Owner: "t"}
	for i := 0; i < 2; i++ {
		if err := reg.Register(process.EgressClassPluginProcess, cfg); err != nil {
			t.Fatalf("registration %d: %v", i+1, err)
		}
	}
	got, ok := reg.reg.Lookup(egress.EgressClass(process.EgressClassPluginProcess))
	if !ok || !got.Enabled || len(got.AllowedTiers) != 1 || got.Owner != "t" {
		t.Fatalf("registered class = %+v (%v), want the translated config", got, ok)
	}
}

func TestTryFlockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.lock")
	a, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if ok, err := tryFlock(a); !ok || err != nil {
		t.Fatalf("first lock = %v, %v", ok, err)
	}
	if ok, err := tryFlock(b); ok || err != nil {
		t.Fatalf("second lock on a held file = %v, %v; want false", ok, err)
	}
	c, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if ok, err := tryFlock(c); ok || err == nil {
		t.Fatalf("lock on a closed file = %v, %v; want an error", ok, err)
	}
}
