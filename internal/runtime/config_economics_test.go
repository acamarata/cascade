package runtime

import (
	"context"
	"testing"
)

// TestConfigEconomicsDefaults: absent [fleet.economics] resolves to the
// 08 §3 shipped defaults.
func TestConfigEconomicsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "")
	cfg, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Economics.ExplorationRate != defaultExplorationRate {
		t.Errorf("ExplorationRate = %v, want default %v", cfg.Economics.ExplorationRate, defaultExplorationRate)
	}
	if cfg.Economics.AllowPremiumInCrunch != defaultAllowPremiumInCrunch {
		t.Errorf("AllowPremiumInCrunch = %v, want default %v", cfg.Economics.AllowPremiumInCrunch, defaultAllowPremiumInCrunch)
	}
}

// TestConfigEconomicsExplicitValues: exploration_rate/allow_premium_in_crunch
// and the multipliers/weights sub-tables parse.
func TestConfigEconomicsExplicitValues(t *testing.T) {
	toml := "[fleet.economics]\n" +
		"exploration_rate = 0.10\n" +
		"allow_premium_in_crunch = true\n" +
		"[fleet.economics.multipliers.\"lead\"]\n" +
		"build = 9.9\n" +
		"[fleet.economics.weights.\"plan\"]\n" +
		"quality = 42\n"
	dir := t.TempDir()
	path := writeConfigFile(t, dir, toml)
	cfg, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Economics.ExplorationRate != 0.10 {
		t.Errorf("ExplorationRate = %v, want 0.10", cfg.Economics.ExplorationRate)
	}
	if !cfg.Economics.AllowPremiumInCrunch {
		t.Error("AllowPremiumInCrunch = false, want true")
	}
	if cfg.Economics.Multipliers["lead"]["build"] != 9.9 {
		t.Errorf("Multipliers[lead][build] = %v, want 9.9", cfg.Economics.Multipliers["lead"]["build"])
	}
	if cfg.Economics.Weights["plan"]["quality"] != 42 {
		t.Errorf("Weights[plan][quality] = %v, want 42", cfg.Economics.Weights["plan"]["quality"])
	}
}

// TestConfigEconomicsUnknownTopKey: an unknown key inside [fleet.economics]
// is a hard typed *ConfigError (validate-before-write), unlike
// [fleet.accounts]'s per-entry leniency.
func TestConfigEconomicsUnknownTopKey(t *testing.T) {
	toml := "[fleet.economics]\n" + "bogus_key = 1\n"
	dir := t.TempDir()
	path := writeConfigFile(t, dir, toml)
	_, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err == nil {
		t.Fatal("want a typed error for an unknown [fleet.economics] key")
	}
	var cfgErr *ConfigError
	if !asConfigError(err, &cfgErr) {
		t.Fatalf("error = %v (%T), want *ConfigError", err, err)
	}
}

// TestConfigEconomicsTypeMismatch: a non-numeric exploration_rate is a
// typed error.
func TestConfigEconomicsTypeMismatch(t *testing.T) {
	toml := "[fleet.economics]\n" + "exploration_rate = \"fast\"\n"
	dir := t.TempDir()
	path := writeConfigFile(t, dir, toml)
	if _, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)}); err == nil {
		t.Fatal("want a typed error for a non-numeric exploration_rate")
	}
}

// asConfigError is a tiny errors.As wrapper kept local to this file so it
// reads at the call site without importing errors twice across sibling
// test files.
func asConfigError(err error, target **ConfigError) bool {
	ce, ok := err.(*ConfigError)
	if ok {
		*target = ce
	}
	return ok
}
