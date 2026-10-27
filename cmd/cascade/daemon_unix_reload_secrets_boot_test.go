//go:build !windows

// Purpose: the daemon's boot-time application of the baseline's effective
// [secrets] thresholds (bootSecrets): a degraded boot never runs a raised
// threshold, and an invalid table or a detector that refuses the values stops
// the daemon instead of being dropped.
// Inputs: a real runtime store and config file under a temp CASCADE_HOME and
// an injected detector setter. Outputs: none (tests).
// SPORT: cmd/cascade daemon tests (P1-SEC-24).
package main

import (
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBootSecretsAppliesDegradedThresholds(t *testing.T) {
	ctx := context.Background()
	paths, clock, store := newReloadTestStore(t)
	body := "[secrets]\nentropy_floor = 3.0\nconfidence_threshold = 0.95\n"
	if err := os.WriteFile(paths.ConfigPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath(), Getenv: func(string) string { return "" }, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	checker := runtime.NewBaselineChecker(store, clock, nil, nil)
	res := checker.CheckFile(ctx, runtime.EffectiveConfigOf(cfg), runtime.BaselineFile{})
	if !res.Outcome.Degraded() {
		t.Fatalf("a raised threshold with no baseline: %s, want a degraded boot", res.Outcome.Detail())
	}
	var got []secrets.DetectionConfig
	err = bootSecretsWith(cfg, &configBaseline{Checker: checker}, func(c secrets.DetectionConfig) error { got = append(got, c); return nil })
	want := secrets.DetectionConfig{EntropyFloor: 3.0, ConfidenceThreshold: secrets.DefaultConfidenceThreshold}
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("detector reloaded with %+v (err %v), want the tightest-of %+v (the raised 0.95 must not run)", got, err, want)
	}
	section, _ := cfg.Extra["secrets"].(map[string]interface{})
	if _, raised := section["confidence_threshold"]; raised || section["entropy_floor"] != 3.0 {
		t.Fatalf("boot config [secrets] = %v, want the effective thresholds", section)
	}
}

// TestBootSecretsRefusesInsteadOfRunningLooser: a detector that will not take
// the effective values, or a [secrets] table it refuses, stops the daemon; the
// error is never dropped.
func TestBootSecretsRefusesInsteadOfRunningLooser(t *testing.T) {
	ctx := context.Background()
	load := func(t *testing.T, body string) (*runtime.Config, *configBaseline) {
		paths, clock, store := newReloadTestStore(t)
		if err := os.WriteFile(paths.ConfigPath(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath(), Getenv: func(string) string { return "" }, Environ: func() []string { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		checker := runtime.NewBaselineChecker(store, clock, nil, nil)
		checker.CheckFile(ctx, runtime.EffectiveConfigOf(cfg), runtime.BaselineFile{})
		return cfg, &configBaseline{Checker: checker}
	}
	t.Run("the detector refuses the values", func(t *testing.T) {
		cfg, b := load(t, "[secrets]\nentropy_floor = 3.0\nconfidence_threshold = 0.95\n")
		err := bootSecretsWith(cfg, b, func(secrets.DetectionConfig) error { return cascade.New(cascade.KindUnavailable, "custody gone") })
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("err = %v, want the set failure returned", err)
		}
	})
	for name, body := range map[string]string{
		"zero threshold":    "[secrets]\nconfidence_threshold = 0\n",
		"string threshold":  "[secrets]\nconfidence_threshold = \"0.8\"\n",
		"unknown key":       "[secrets]\nconfidence_treshold = 0.9\n",
		"floor above eight": "[secrets]\nentropy_floor = 9.0\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, b := load(t, body)
			called := false
			err := bootSecretsWith(cfg, b, func(secrets.DetectionConfig) error { called = true; return nil })
			if !cascade.HasKind(err, cascade.KindInvalidInput) || called {
				t.Fatalf("err = %v, detector reached = %v: an invalid [secrets] must stop the boot before the detector", err, called)
			}
		})
	}
}

// TestWireBackgroundSubsystemsReturnsTheBootSecretsError: the daemon composition
// stops on a boot that cannot apply the effective thresholds. The error comes
// back before anything else is wired (every other argument is zero).
func TestWireBackgroundSubsystemsReturnsTheBootSecretsError(t *testing.T) {
	ctx := context.Background()
	paths, clock, store := newReloadTestStore(t)
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath(), Getenv: func(string) string { return "" }, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	checker := runtime.NewBaselineChecker(store, clock, nil, nil)
	checker.CheckFile(ctx, runtime.EffectiveConfigOf(cfg), runtime.BaselineFile{})

	prev := setProcessDetection
	t.Cleanup(func() { setProcessDetection = prev })
	setProcessDetection = func(secrets.DetectionConfig) error { return cascade.New(cascade.KindUnavailable, "detector refuses") }

	admin, pol, cleanup, err := wireBackgroundSubsystems(ctx, nil, nil, daemonDeps{}, cfg, nil, nil, nil, nil, &configBaseline{Checker: checker})
	if !cascade.HasKind(err, cascade.KindUnavailable) || admin != nil || pol != nil || cleanup != nil {
		t.Fatalf("wireBackgroundSubsystems = (%v, %v, %v, %v), want the boot error and nothing wired", admin, pol, cleanup != nil, err)
	}
}
