// Purpose: the registered guarded families end to end: a hot reload of a
// loosening returns ElevationRequired through the real HotReloader, a
// restart runs the degraded value through the real BaselineChecker and
// ApplyEffective (the seam the daemon boot uses), and the consumers read the
// admitted value back out of the config; a tightening applies at once.
// Inputs: temp config files, an in-memory store, a fixed clock.
// SPORT: cmd/cascade guarded families tests.
package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

var guardedClock = runtime.NewFixedClock(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))

func guardedOpts(path string) runtime.LoadOptions {
	return runtime.LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}
}

// loadBody writes body as a fresh config.toml and loads it.
func loadBody(t *testing.T, body string) (*runtime.Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := runtime.Load(context.Background(), guardedOpts(path))
	if err != nil {
		t.Fatalf("load %q: %v", body, err)
	}
	return cfg, path
}

// reload runs before, then a real hot reload to after, with no elevation
// helper enrolled (a loosening can only be refused).
func reload(t *testing.T, before, after string) runtime.ReloadOutcome {
	t.Helper()
	cfg, path := loadBody(t, before)
	hr := runtime.NewHotReloader(path, guardedOpts(path), cfg, guardedClock, nil, nil, nil)
	if err := os.WriteFile(path, []byte(after), 0o600); err != nil {
		t.Fatal(err)
	}
	return hr.Reload(context.Background())
}

func pathKeys(paths []runtime.LooseningPath) []string {
	var keys []string
	for _, p := range paths {
		keys = append(keys, p.Key)
	}
	return keys
}

// expectLoosening: before -> after is refused for want; after -> before
// (the removal or lowering) applies at once.
func expectLoosening(t *testing.T, before, after, want string) {
	t.Helper()
	if out := reload(t, before, after); !out.ElevationRequired || !slices.Contains(pathKeys(out.LooseningPaths), want) {
		t.Fatalf("reload %q -> %q: elevation=%v paths=%v, want ElevationRequired naming %s", before, after, out.ElevationRequired, pathKeys(out.LooseningPaths), want)
	}
	if out := reload(t, after, before); !out.Accepted {
		t.Fatalf("reload %q -> %q (a tightening) refused: %+v", after, before, out)
	}
}

// restart boots accepted (recording the baseline), then boots edited against
// the same store the way the daemon does: CheckFile, then ApplyEffective when
// the admitted values differ from the file's. It returns the outcome and the
// config every consumer reads.
func restart(t *testing.T, accepted, edited *runtime.Config, file runtime.BaselineFile) (runtime.BaselineResult, *runtime.Config, *runtime.BaselineChecker) {
	t.Helper()
	store := storetest.NewMemStore()
	first := runtime.NewBaselineChecker(store, guardedClock, nil, nil).CheckFile(context.Background(), runtime.EffectiveConfigOf(accepted), runtime.BaselineFile{})
	if first.Outcome.Degraded() {
		t.Fatalf("baseline boot degraded: %s", first.Outcome.Detail())
	}
	checker := runtime.NewBaselineChecker(store, guardedClock, nil, runtime.NewStoreAuditRecorder(store, guardedClock))
	onDisk := runtime.EffectiveConfigOf(edited)
	res := checker.CheckFile(context.Background(), onDisk, file)
	if runtime.ComputeSectionsHash(res.Effective) != runtime.ComputeSectionsHash(onDisk) {
		runtime.ApplyEffective(edited, res.Effective)
	}
	return res, edited, checker
}

func verdictL1(t *testing.T, cfg *runtime.Config) policy.Verdict {
	t.Helper()
	c := policy.NewController(nil)
	if err := applyAutonomyProfile(context.Background(), c, cfg); err != nil {
		t.Fatal(err)
	}
	return c.VerdictFor(policy.L1)
}

func TestPolicyOverlayLooseningNeedsAttestation(t *testing.T) {
	const overlay = "[policy]\nallow = [\"L1\"]\n"
	expectLoosening(t, "", overlay, "policy.allow")
	accepted, _ := loadBody(t, "")
	edited, _ := loadBody(t, overlay)
	if verdictL1(t, edited) != policy.VerdictAllow {
		t.Fatal("stub check: the overlay as written does not allow L1, so this test proves nothing")
	}
	res, cfg, _ := restart(t, accepted, edited, runtime.BaselineFile{})
	if res.Outcome.Detail() != "baseline_divergent" {
		t.Fatalf("restart with a hand-added allow overlay: %s, want baseline_divergent", res.Outcome.Detail())
	}
	if parsed, err := policy.ParseConfig(cfg.Extra); err != nil || len(parsed.Overlays[policy.VerdictAllow]) != 0 {
		t.Fatalf("written-back [policy] = %v (err %v), want no allow overlay", cfg.Extra["policy"], err)
	}
	if v := verdictL1(t, cfg); v == policy.VerdictAllow {
		t.Fatal("the degraded daemon still runs the allow overlay")
	}
}

func TestAutoAdvanceCeilingRaiseIsLoosening(t *testing.T) {
	expectLoosening(t, "", "[policy]\nauto_advance_ceiling = \"tier1\"\n", "policy.auto_advance_ceiling")
	expectLoosening(t, "[policy]\nauto_advance_ceiling = \"tier1\"\n", "[policy]\nauto_advance_ceiling = \"tier3\"\n", "policy.auto_advance_ceiling")
}

func TestAgentsEgressAllowAdditionIsLoosening(t *testing.T) {
	table := "[agents.egress." + string(egressproxy.DriverClaude) + "]\nallow = "
	key := "agents.egress." + string(egressproxy.DriverClaude) + ".allow"
	expectLoosening(t, "", table+"[\"api.example.test:443\"]\n", key)
	expectLoosening(t, table+"[\"api.example.test:443\"]\n", table+"[\"api.example.test:443\", \"cdn.example.test:443\"]\n", key)
}

func TestRestrictedExternalLanesAdditionIsLoosening(t *testing.T) {
	const key = "conductor.restricted_external_lanes"
	expectLoosening(t, "", "[conductor]\nrestricted_external_lanes = [\"lane-a\"]\n", key)
	expectLoosening(t, "[conductor]\nrestricted_external_lanes = [\"lane-a\"]\n", "[conductor]\nrestricted_external_lanes = [\"lane-a\", \"lane-b\"]\n", key)
}

// TestBaselineUpgradeUnchangedConfigNotDivergent: a baseline written by a
// binary without families (no Families in the hash or the snapshot) still
// admits the unchanged config; a newly modeled key at its default is absent,
// and the hash equals the one the pre-families binary recorded.
func TestBaselineUpgradeUnchangedConfigNotDivergent(t *testing.T) {
	cfg, _ := loadBody(t, "[policy]\napproval_batch_cap = 20\n[nodes]\ntrust_tier = \"\"\n[conductor]\nrestricted_external_lanes = []\n")
	eff := runtime.EffectiveConfigOf(cfg)
	if eff.Families != nil {
		t.Fatalf("families at their defaults extracted as %s, want none", eff.Families)
	}
	// The hash a binary without families recorded for this config (computed at
	// the parent commit): any change to the hash breaks every upgraded install.
	if h := runtime.ComputeSectionsHash(eff); h != "b9ef51fb835f8fbf08b7823d7ae1bbf36c608962560ed17b9a7ca66bff001fd2" {
		t.Fatalf("hash %s differs from the pre-families baseline hash", h)
	}
	store := storetest.NewMemStore()
	if r := runtime.NewBaselineChecker(store, guardedClock, nil, nil).CheckFile(context.Background(), eff, runtime.BaselineFile{}); r.Outcome.Degraded() {
		t.Fatal(r.Outcome.Detail())
	}
	again, _ := loadBody(t, "[policy]\napproval_batch_cap = 20\n[nodes]\ntrust_tier = \"\"\n[conductor]\nrestricted_external_lanes = []\n")
	if r := runtime.NewBaselineChecker(store, guardedClock, nil, nil).CheckFile(context.Background(), runtime.EffectiveConfigOf(again), runtime.BaselineFile{}); r.Outcome.Detail() != "baseline_ok" {
		t.Fatalf("unchanged config after the upgrade: %s, want baseline_ok", r.Outcome.Detail())
	}
}

// TestDegradedBootKeepsTighterLimits: a lane ceiling and an ask overlay are
// tighter than their absent defaults, so a degraded boot keeps them; raising
// or removing a ceiling is a loosening, lowering one applies at once.
func TestDegradedBootKeepsTighterLimits(t *testing.T) {
	const ceiling = "[conductor.quota]\nceiling = { lane-a = 5 }\n"
	expectLoosening(t, ceiling, "[conductor.quota]\nceiling = { lane-a = 9 }\n", "conductor.quota.ceiling.lane-a")
	expectLoosening(t, ceiling, "[conductor.quota]\n", "conductor.quota.ceiling.lane-a")
	accepted, _ := loadBody(t, "")
	edited, _ := loadBody(t, ceiling+"[policy]\nask = [\"L0\"]\nallow = [\"L1\"]\n")
	res, cfg, _ := restart(t, accepted, edited, runtime.BaselineFile{})
	q, _, err := conductor.ParseQuotaConfig(cfg.Extra)
	if res.Outcome.Detail() != "baseline_divergent" || err != nil || q.CeilingOverrides["lane-a"] != 5 {
		t.Fatalf("degraded boot: %s, ceilings %v (err %v), want baseline_divergent keeping lane-a = 5", res.Outcome.Detail(), q.CeilingOverrides, err)
	}
	c := policy.NewController(nil)
	if err := applyAutonomyProfile(context.Background(), c, cfg); err != nil || c.VerdictFor(policy.L0) != policy.VerdictAsk {
		t.Fatalf("degraded boot runs L0 as %v (err %v), want the operator's ask overlay kept", c.VerdictFor(policy.L0), err)
	}
	// askKeep is exactly the levels the fallback profile does not deny.
	strict, _ := policy.ParseConfig(map[string]interface{}{"policy": map[string]interface{}{
		"autonomy_profile": runtime.RestrictiveFallback(runtime.EffectiveConfig{}).Policy.AutonomyProfile}})
	p, err := policy.Resolve(strict)
	for l := policy.L0; err == nil && l <= policy.L4; l++ {
		if kept := slices.Contains(askKeep, interface{}(l.String())); kept != (p.VerdictFor(l) != policy.VerdictDeny) {
			t.Fatalf("askKeep has %s = %v, but the fallback profile runs it as %v", l, kept, p.VerdictFor(l))
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestClipboardTTLRaiseIsLoosening(t *testing.T) {
	expectLoosening(t, "[secrets]\nclipboard_ttl = \"10s\"\n", "[secrets]\nclipboard_ttl = \"60s\"\n", "secrets.clipboard_ttl")
	accepted, _ := loadBody(t, "")
	edited, _ := loadBody(t, "[secrets]\nclipboard_ttl = \"60s\"\n")
	res, cfg, _ := restart(t, accepted, edited, runtime.BaselineFile{})
	ran, err := runtime.SecretsClipboardTTL(runtime.EffectiveConfigOf(cfg))
	if res.Outcome.Detail() != "baseline_divergent" || err != nil || ran != runtime.DefaultSecretsClipboardTTL {
		t.Fatalf("restart with a raised clipboard_ttl: %s, runs %v (err %v), want baseline_divergent running the old 30s", res.Outcome.Detail(), ran, err)
	}
	// A degraded boot keeps an operator's tighter TTL.
	tight, _ := loadBody(t, "[secrets]\nclipboard_ttl = \"10s\"\n[policy]\nallow = [\"L1\"]\n")
	_, cfg, _ = restart(t, accepted, tight, runtime.BaselineFile{})
	if ran, _ := runtime.SecretsClipboardTTL(runtime.EffectiveConfigOf(cfg)); ran != 10*time.Second {
		t.Fatalf("degraded boot ran clipboard_ttl %v, want the tighter 10s kept", ran)
	}
}

// withSocket returns a config holding retrieval.embedding.local_socket. The
// [retrieval] decoder does not accept [retrieval.embedding] until the local
// embedding lane lands, so the table is set in memory, not loaded.
func withSocket(t *testing.T, socket string) *runtime.Config {
	cfg, _ := loadBody(t, "")
	if socket != "" {
		cfg.Extra["retrieval"] = map[string]interface{}{"embedding": map[string]interface{}{"local_socket": socket}}
	}
	return cfg
}

func TestLocalSocketChangeIsLoosening(t *testing.T) {
	const key = "retrieval.embedding.local_socket"
	eff := func(s string) runtime.EffectiveConfig { return runtime.EffectiveConfigOf(withSocket(t, s)) }
	for _, c := range [][2]string{{"", "/run/embed-a.sock"}, {"/run/embed-a.sock", "/run/embed-b.sock"}} {
		if p := runtime.CompareSecurity(eff(c[0]), eff(c[1])); !slices.Contains(pathKeys(p), key) {
			t.Fatalf("%q -> %q: paths %v, want %s (the reload gate refuses it)", c[0], c[1], pathKeys(p), key)
		}
	}
	if p := runtime.CompareSecurity(eff("/run/embed-a.sock"), eff("")); len(p) != 0 {
		t.Fatalf("unsetting local_socket: paths %v, want a tightening", pathKeys(p))
	}
	edited := withSocket(t, "/run/embed-a.sock")
	onDisk := runtime.EffectiveConfigOf(edited)
	res, cfg, checker := restart(t, withSocket(t, ""), edited, runtime.BaselineFile{})
	if s, err := runtime.RetrievalEmbeddingLocalSocket(runtime.EffectiveConfigOf(cfg)); res.Outcome.Detail() != "baseline_divergent" || err != nil || s != "" {
		t.Fatalf("restart with a new local_socket: %s, runs %q (err %v), want baseline_divergent running the old (unset) value", res.Outcome.Detail(), s, err)
	}
	digest := runtime.ConfigDigest([]byte("fixture config bytes"))
	checker.SetVerifier(func(runtime.BaselineProof) error { return nil })
	proof := runtime.BaselineProof{Method: runtime.AcceptLooseningMethod, RawParams: []byte(`{"config_sha256":"` + digest + `"}`), Attestation: []byte(`{"fixture":true}`)}
	if err := checker.Accept(context.Background(), onDisk, proof); err != nil {
		t.Fatal(err)
	}
	file := runtime.BaselineFile{SHA256: digest, Signed: func() (runtime.EffectiveConfig, bool) { return onDisk, true }}
	if res := checker.CheckFile(context.Background(), onDisk, file); res.Outcome.Detail() != "baseline_accepted" {
		t.Fatalf("after config.accept_loosening: %s, want baseline_accepted", res.Outcome.Detail())
	} else if s, _ := runtime.RetrievalEmbeddingLocalSocket(res.Effective); s != "/run/embed-a.sock" {
		t.Fatalf("accepted local_socket runs %q", s)
	}
}

func TestLocalSocketKnownOnlyViaFamily(t *testing.T) {
	for _, k := range []string{"retrieval.embedding.local_socket", "secrets.entropy_floor", "secrets.confidence_threshold", "policy.allow"} {
		if _, err := runtime.ResolveDottedPath(k); err != nil {
			t.Fatalf("%s is not a known key: %v", k, err)
		}
	}
	if _, err := runtime.ResolveDottedPath("retrieval.embedding.model"); err == nil {
		t.Fatal("an unmodeled retrieval.embedding key is known")
	}
	before, after := withSocket(t, ""), withSocket(t, "/run/embed-a.sock")
	if p := runtime.CompareSecurity(runtime.EffectiveConfigOf(before), runtime.EffectiveConfigOf(after)); !slices.Contains(pathKeys(p), "retrieval.embedding.local_socket") {
		t.Fatalf("setting the key is not routed to the loosening path: %v", pathKeys(p))
	}
}

// TestGuardedExtractorsCoverDecoderFields: each family extractor names every
// field its decoder returns, so a new decoder field fails here until it is
// modeled (or deliberately left to a built-in EffectiveConfig field).
func TestGuardedExtractorsCoverDecoderFields(t *testing.T) {
	for typ, want := range map[reflect.Type][]string{
		reflect.TypeOf(policy.Config{}):         {"ProfileName", "Overlays", "Batching", "RiskGates", "AutoAdvanceCeiling"},
		reflect.TypeOf(nodes.Section{}):         {"DefaultTrustTier", "KnownHostsPath", "ScanLAN", "DiscoveryNetworks", "DispatchRepoRoot", "DispatchRemote", "Travel"},
		reflect.TypeOf(conductor.QuotaConfig{}): {"SpillOrder", "CeilingOverrides", "PersonalTracking"},
	} {
		var got []string
		for i := range typ.NumField() {
			got = append(got, typ.Field(i).Name)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s fields %v, the extractor models %v: model the new field", typ, got, want)
		}
	}
}
