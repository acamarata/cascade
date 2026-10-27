package runtime_test

// Purpose: the baseline side of the guarded [secrets] thresholds: a snapshot
// written before they were guarded stays valid, a degraded boot keeps only a
// tighter threshold, an explicit default hashes like an absent key, and a
// non-number is encodable and named. Rig and helpers: hotreload_secrets_test.go.
// SPORT: runtime/boot-baseline-check tests (P1-SEC-24).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// v1 is the guarded view as the build before this one wrote it: [secrets]
// held only keychain_backend. Its hash is computed here from the documented
// encoding (name, NUL, JSON, NUL per section), not by ComputeSectionsHash.
func v1Hash(secretsJSON string) string {
	h := sha256.New()
	for _, part := range [][2]string{
		{"policy", `{"AutonomyProfile":""}`}, {"secrets", secretsJSON}, {"sync", `[]`},
		{"nodes", `{"TrustTier":""}`}, {"conductor", `{"ExternalRoutingEnabled":false,"SpillEnabled":false}`},
		{"elevation", `{"AllowRemote":false,"HelperPubkey":""}`},
	} {
		h.Write([]byte(part[0] + "\x00" + part[1] + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func seedV1(t *testing.T, store *storetest.MemStore) {
	t.Helper()
	rec := fmt.Sprintf(`{"v":1,"sections_hash":%q,"sections":["policy","secrets","sync","nodes","conductor","elevation"]}`, v1Hash(`{"KeychainBackend":""}`))
	snap := `{"Policy":{"AutonomyProfile":""},"Secrets":{"KeychainBackend":""},"Sync":{"Classes":{}},"Nodes":{"TrustTier":""},"Conductor":{"ExternalRoutingEnabled":false,"SpillEnabled":false},"Elevation":{"AllowRemote":false,"HelperPubkey":""}}`
	ctx := context.Background()
	if err := store.Put(ctx, "audit", "config_baseline", []byte(rec)); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "audit", "config_baseline_snapshot", []byte(snap)); err != nil {
		t.Fatal(err)
	}
}

func bootWith(t *testing.T, body string) (runtime.BaselineResult, *runtime.BaselineChecker) {
	t.Helper()
	store := storetest.NewMemStore()
	seedV1(t, store)
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	bc := runtime.NewBaselineChecker(store, clock, &eventLog{}, runtime.NewStoreAuditRecorder(store, clock))
	r := newRig(t, body)
	return bc.CheckFile(context.Background(), runtime.EffectiveConfigOf(r.hr.Current()), runtime.BaselineFile{}), bc
}

func TestBaselineSnapshotWithoutThresholdsIsNotDivergent(t *testing.T) {
	if res, _ := bootWith(t, ""); res.Outcome != runtime.BaselineOK {
		t.Fatalf("an unchanged config over a v1 snapshot: %s, want baseline_ok", res.Outcome.Detail())
	}
	// An explicit default is the same value as an absent key.
	if res, _ := bootWith(t, "[secrets]\nentropy_floor = 3.5\nconfidence_threshold = 0.8\n"); res.Outcome != runtime.BaselineOK {
		t.Fatalf("explicit defaults over a v1 snapshot: %s, want baseline_ok", res.Outcome.Detail())
	}
	if res, _ := bootWith(t, "[secrets]\nentropy_floor = 3.0\n"); res.Outcome != runtime.BaselineTightened {
		t.Fatalf("a lowered floor over a v1 snapshot: %s, want baseline_tightened", res.Outcome.Detail())
	}
	// A raised threshold was never attested: the old build let it through,
	// this one does not.
	if res, _ := bootWith(t, "[secrets]\nconfidence_threshold = 0.95\n"); res.Outcome != runtime.BaselineDivergent {
		t.Fatalf("a raised threshold over a v1 snapshot: %s, want baseline_divergent", res.Outcome.Detail())
	}
}

func TestDegradedKeepsTighterThreshold(t *testing.T) {
	// Divergent for an unrelated reason (routing opened without a signed
	// acceptance); the floor was lowered and the threshold raised.
	body := "[conductor]\nexternal_routing_enabled = true\n[secrets]\nentropy_floor = 3.0\nconfidence_threshold = 0.95\n"
	res, bc := bootWith(t, body)
	if res.Outcome != runtime.BaselineDivergent {
		t.Fatalf("outcome %s, want baseline_divergent", res.Outcome.Detail())
	}
	sec := res.Effective.Secrets
	if sec.EntropyFloor == nil || *sec.EntropyFloor != 3.0 {
		t.Fatalf("degraded entropy floor %v, want the on-disk 3.0 kept", sec.EntropyFloor)
	}
	if sec.ConfidenceThreshold != nil {
		t.Fatalf("degraded threshold %v, want the shipped default (nil), never the raised 0.95", *sec.ConfidenceThreshold)
	}
	if boot, ok := bc.BootEffective(); !ok || boot.Secrets.EntropyFloor == nil {
		t.Fatalf("BootEffective = %+v, %v", boot, ok)
	}
	// The degraded values must be writable back into the config and still
	// parse: a zero threshold would be refused by the detector.
	cfg, _ := runtime.Load(context.Background(), newRig(t, body).opts)
	runtime.ApplySecretsThresholds(cfg, sec)
	section, _ := cfg.Extra["secrets"].(map[string]interface{})
	parsed, err := secrets.ParseDetectionConfig(section)
	if err != nil || parsed.EntropyFloor != 3.0 || parsed.ConfidenceThreshold != secrets.DefaultConfidenceThreshold {
		t.Fatalf("degraded section %v parses to %+v (%v)", section, parsed, err)
	}
}

func TestSecretsHashIgnoresExplicitDefaults(t *testing.T) {
	hash := func(body string) string {
		return runtime.ComputeSectionsHash(runtime.EffectiveConfigOf(newRig(t, body).hr.Current()))
	}
	if hash("") != hash("[secrets]\nentropy_floor = 3.5\nconfidence_threshold = 0.8\n") {
		t.Fatal("explicit defaults hash differently from absent keys")
	}
	if hash("") == hash("[secrets]\nentropy_floor = 3.0\n") {
		t.Fatal("a lowered floor does not change the guarded hash")
	}
	if hash("[secrets]\nkeychain_backend = \"x\"\n") != v1Hash(`{"KeychainBackend":"x"}`) {
		t.Fatal("a config with no thresholds no longer hashes as the v1 shape")
	}
}

// loadEffective loads body as a config file and returns its guarded view,
// without requiring the [secrets] table to be valid.
func loadEffective(t *testing.T, body string) runtime.EffectiveConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := runtime.Load(context.Background(), runtime.LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return runtime.EffectiveConfigOf(cfg)
}

// TestNonNumberThresholdsAreEncodableAndNamed: nan and inf are not JSON, so
// a guarded view carrying one would hash as if the section were empty. They
// read as one finite fail-closed value instead, and the refusal says so.
func TestNonNumberThresholdsAreEncodableAndNamed(t *testing.T) {
	current := loadEffective(t, "[secrets]\nentropy_floor = 3.0\n")
	for _, value := range []string{"nan", "inf", "-inf", "\"x\""} {
		proposed := loadEffective(t, "[secrets]\nentropy_floor = "+value+"\n")
		// The view is not persistable, but it must still hash to a value no valid
		// view has, and must still be refused as a loosening that says so.
		if runtime.ComputeSectionsHash(proposed) == runtime.ComputeSectionsHash(current) {
			t.Fatalf("entropy_floor = %s hashes like a valid view", value)
		}
		paths := runtime.CompareSecurity(current, proposed)
		if len(paths) != 1 || paths[0].Proposed != "invalid" {
			t.Fatalf("entropy_floor = %s: %+v, want one loosening that says it is invalid", value, paths)
		}
	}
}

// TestThresholdRangesMatchTheDetector pins the guard's ranges to the
// detector's own validation: a value the detector refuses must read as
// invalid, and one it accepts must not.
func TestThresholdRangesMatchTheDetector(t *testing.T) {
	for _, key := range []string{"entropy_floor", "confidence_threshold"} {
		for _, value := range []string{"-0.1", "0", "0.001", "1", "1.001", "7.99", "8", "8.001", "9"} {
			body := "[secrets]\n" + key + " = " + value + "\n"
			_, parseErr := secrets.ParseDetectionConfig(map[string]interface{}{key: mustFloat(t, value)})
			sec := loadEffective(t, body).Secrets
			p := sec.EntropyFloor
			if key == "confidence_threshold" {
				p = sec.ConfidenceThreshold
			}
			invalid := p != nil && *p == math.MaxFloat64
			if invalid != (parseErr != nil) {
				t.Errorf("%s = %s: guard says invalid=%v, detector validation error=%v", key, value, invalid, parseErr)
			}
		}
	}
}

func mustFloat(t *testing.T, v string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestInvalidThresholdsNeverReachTheBaseline: a boot with a threshold the
// detector refuses (it stops later at the firewall wiring) must neither be
// attested into the baseline nor leave the daemon needing a signed acceptance
// once the typo is fixed. The record and snapshot stay byte-identical.
func TestInvalidThresholdsNeverReachTheBaseline(t *testing.T) {
	bodies := map[string]string{
		"zero threshold":      "[secrets]\nconfidence_threshold = 0\n",
		"negative floor":      "[secrets]\nentropy_floor = -1\n",
		"floor above eight":   "[secrets]\nentropy_floor = 9.0\n",
		"threshold above one": "[secrets]\nconfidence_threshold = 1.5\n",
		"string threshold":    "[secrets]\nconfidence_threshold = \"0.8\"\n",
	}
	for name, body := range bodies {
		t.Run(name+" over a v1 baseline", func(t *testing.T) { invalidOverV1Baseline(t, body) })
		t.Run(name+" on a first boot", func(t *testing.T) { invalidOnFirstBoot(t, body) })
	}
}

func newBootChecker(store *storetest.MemStore) *runtime.BaselineChecker {
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	return runtime.NewBaselineChecker(store, clock, &eventLog{}, runtime.NewStoreAuditRecorder(store, clock))
}

func invalidOverV1Baseline(t *testing.T, body string) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	seedV1(t, store)
	rec0, _ := store.Get(ctx, "audit", "config_baseline")
	snap0, _ := store.Get(ctx, "audit", "config_baseline_snapshot")
	bc := newBootChecker(store)

	res := bc.CheckFile(ctx, loadEffective(t, body), runtime.BaselineFile{})
	if res.Outcome != runtime.BaselineDivergent {
		t.Fatalf("invalid threshold: %s, want baseline_divergent (fail closed)", res.Outcome.Detail())
	}
	if res.Effective.Secrets.EntropyFloor != nil || res.Effective.Secrets.ConfidenceThreshold != nil {
		t.Fatalf("degraded Effective carries thresholds %+v, want the shipped defaults", res.Effective.Secrets)
	}
	rec1, _ := store.Get(ctx, "audit", "config_baseline")
	snap1, _ := store.Get(ctx, "audit", "config_baseline_snapshot")
	if !bytes.Equal(rec0, rec1) || !bytes.Equal(snap0, snap1) {
		t.Fatal("the invalid config changed the baseline record or snapshot")
	}
	if res := bc.CheckFile(ctx, loadEffective(t, ""), runtime.BaselineFile{}); res.Outcome != runtime.BaselineOK {
		t.Fatalf("typo fixed to the default: %s, want baseline_ok, not a divergence", res.Outcome.Detail())
	}
	if res := bc.CheckFile(ctx, loadEffective(t, "[secrets]\nentropy_floor = 3.0\n"), runtime.BaselineFile{}); res.Outcome != runtime.BaselineTightened {
		t.Fatalf("typo fixed to a tighter value: %s, want baseline_tightened", res.Outcome.Detail())
	}
}

func invalidOnFirstBoot(t *testing.T, body string) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	bc := newBootChecker(store)
	res := bc.CheckFile(ctx, loadEffective(t, body), runtime.BaselineFile{})
	if res.Outcome != runtime.BaselineMissing {
		t.Fatalf("invalid threshold on a first boot: %s, want baseline_missing (nothing recorded)", res.Outcome.Detail())
	}
	if _, err := store.Get(ctx, "audit", "config_baseline"); err == nil {
		t.Fatal("the invalid config was recorded as the first baseline")
	}
	if res := bc.CheckFile(ctx, loadEffective(t, ""), runtime.BaselineFile{}); res.Outcome != runtime.BaselineFirstBootAccepted {
		t.Fatalf("typo fixed: %s, want the first boot accepted", res.Outcome.Detail())
	}
}
