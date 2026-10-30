package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/runtime"
)

func TestGuardedConfigGateCoversGateways(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	cfg := &runtime.Config{Extra: map[string]interface{}{"gateways": map[string]interface{}{
		"alpha": map[string]interface{}{"base_url": "https://gateway.invalid", "key_env": "TEST_KEY"},
	}}}
	eff := runtime.EffectiveConfigOf(cfg)
	accounts, err := gateway.AccountsFrom(eff)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("registration failed: %v", err)
	}
	paths := runtime.CompareSecurity(runtime.MostRestrictiveDefaults(), eff)
	if !hasGatewayLoosening(paths) {
		t.Fatal("account addition not guarded")
	}
	runtime.ApplyEffective(cfg, runtime.MostRestrictiveDefaults())
	if a, err := gateway.AccountsFrom(runtime.EffectiveConfigOf(cfg)); err != nil || len(a) != 0 {
		t.Fatalf("unadmitted account survived write-back: %v", err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	findings, err := build.UnmodeledGuardedKeys(root)
	if err != nil || len(findings) != 0 {
		t.Fatalf("guarded reads: %v, %v", findings, err)
	}
}

func hasGatewayLoosening(paths []runtime.LooseningPath) bool {
	for _, p := range paths {
		if p.Family == "gateways" {
			return true
		}
	}
	return false
}

func TestGatewaysEnvOverrideIsLoosening(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	opts := runtime.LoadOptions{Path: filepath.Join(dir, "absent.toml"),
		Getenv: func(string) string { return "" },
		Environ: func() []string {
			return []string{
				"CASCADE_GATEWAYS__ALPHA__BASE_URL=https://gateway.invalid",
				"CASCADE_GATEWAYS__ALPHA__KEY_ENV=TEST_KEY",
			}
		},
	}
	cfg, err := runtime.Load(context.Background(), opts)
	if err != nil {
		// A refusal naming the family is the other safe outcome; any other
		// load failure must not pass the test silently.
		if !strings.Contains(err.Error(), "gateways") {
			t.Fatalf("load failed for an unrelated reason: %v", err)
		}
		return
	}
	eff := runtime.EffectiveConfigOf(cfg)
	if !hasGatewayLoosening(runtime.CompareSecurity(runtime.MostRestrictiveDefaults(), eff)) {
		t.Fatal("environment account admitted without a loosening")
	}
	runtime.ApplyEffective(cfg, runtime.MostRestrictiveDefaults())
	if a, err := gateway.AccountsFrom(runtime.EffectiveConfigOf(cfg)); err != nil || len(a) != 0 {
		t.Fatalf("environment account survived restricted write-back: %v", err)
	}
}

func TestGuardedGatewaysRoundTrip(t *testing.T) {
	section := map[string]interface{}{"alpha": map[string]interface{}{
		"base_url": "https://gateway.invalid", "key_env": "TEST_KEY", "models": []any{"model-a"},
		"usage": map[string]interface{}{"source": "endpoint", "window": "day", "path": "/usage", "used_pointer": "/used"},
	}}
	raw, err := extractGatewaysFamily(map[string]interface{}{"gateways": section})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &runtime.Config{Extra: map[string]interface{}{}}
	applyGatewaysFamily(cfg.Extra, raw)
	again := runtime.EffectiveConfigOf(cfg).Families["gateways"]
	if string(raw) != string(again) {
		t.Fatal("admitted account changed during write-back")
	}
	if _, err := extractGatewaysFamily(map[string]interface{}{"gateways": 42}); err == nil {
		t.Fatal("invalid section accepted")
	}
	if _, err := compareGatewaysFamily(json.RawMessage("{"), nil); err == nil {
		t.Fatal("invalid baseline accepted")
	}
	if _, err := compareGatewaysFamily(nil, json.RawMessage("{")); err == nil {
		t.Fatal("invalid proposal accepted")
	}
}
