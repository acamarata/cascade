// Purpose (this file): tests for config.go's ConfigApplier seam.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: restores configApplier after each test (package-level var).
// SPORT: plugins/nself config (ADD) — P1-E25-W5-S103-T1.
package nself

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestConfigApplierDefaultRefuses(t *testing.T) {
	prev := configApplier
	configApplier = refusingApplier{}
	defer func() { configApplier = prev }()

	_, err := configApplier.ApplyDiff(context.Background(), "cascade-nself", []ConfigEntry{
		{Path: "runtime.profile", Literal: `"server"`},
	})
	if err == nil {
		t.Fatal("expected an error from the default refusing applier")
	}
	if !errors.Is(err, ErrNoConfigApplier) {
		t.Fatalf("expected ErrNoConfigApplier, got %v", err)
	}
	var cerr *cascade.Error
	if !errors.As(err, &cerr) || cerr.Kind != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable, got %v", err)
	}
}

func TestSetConfigApplierRefusesNil(t *testing.T) {
	prev := configApplier
	defer func() { configApplier = prev }()

	err := SetConfigApplier(nil)
	if err == nil {
		t.Fatal("expected an error for a nil applier")
	}
	var cerr *cascade.Error
	if !errors.As(err, &cerr) || cerr.Kind != cascade.KindInvalidInput {
		t.Fatalf("expected KindInvalidInput, got %v", err)
	}
	if configApplier == nil {
		t.Fatal("configApplier must never become nil")
	}
}

// recordingApplier is a test double used by other test files in this
// package (handshake_test.go) to assert the tool path never calls
// ApplyDiff.
type recordingApplier struct {
	calls  int
	result ConfigResult
	err    error
}

func (r *recordingApplier) ApplyDiff(_ context.Context, _ string, _ []ConfigEntry) (ConfigResult, error) {
	r.calls++
	return r.result, r.err
}

func TestSetConfigApplierInstallsRealApplier(t *testing.T) {
	prev := configApplier
	defer func() { configApplier = prev }()

	rec := &recordingApplier{}
	if err := SetConfigApplier(rec); err != nil {
		t.Fatalf("SetConfigApplier: %v", err)
	}
	if _, err := configApplier.ApplyDiff(context.Background(), "cascade-nself", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("expected the installed applier to be called, got %d calls", rec.calls)
	}
}
