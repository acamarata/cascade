package egress

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestFleetDiscoveryClassOnce asserts EgressClassFleetDiscovery appears
// exactly once in defaultClasses and no registration function is called.
func TestFleetDiscoveryClassOnce(t *testing.T) {
	count := 0
	for _, entry := range defaultClasses {
		if entry.Class == EgressClassFleetDiscovery {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("EgressClassFleetDiscovery appears %d times in defaultClasses, want 1", count)
	}
}

// TestEgressClassesR21265Registered asserts each shipped class's
// identifier, owner and enabled/disabled verdict. The expectations are
// written out here rather than read back from defaultClasses, so a change
// to the table has to be made in two places on purpose.
func TestEgressClassesR21265Registered(t *testing.T) {
	cases := []struct {
		class           EgressClass
		id              string
		owner           string
		enabled         bool
		allowRestricted bool
	}{
		{EgressClassPluginHostHTTP, "plugin-host-http", "P1-PLG-08", true, false},
		{EgressClassMCP, "mcp.response", "D/S-06.T6", true, false},
		{EgressClassHook, "hook.response", "C/S-05.T1", true, false},
		{EgressClassTelemetry, "telemetry", "P1-E08-W2-S16-T1", false, false},
		{EgressClassOAuth, "oauth", "H/S-15.T2", true, false},
		{EgressClassProviderIntake, "provider-intake", "J/S-20.T1", true, false},
		{EgressClassSpikeMeasurement, "spike-measurement", "F/S-12.T5", true, false},
		{EgressClassBackupTarget, "backup-target", "S/S-41.T3", true, true},
		{EgressClassPluginRemote, "plugin-remote", "O/S-33.T4", false, false},
		{EgressClassRegistryFetch, "registry-fetch", "X/S-50.T1", true, false},
		{EgressClassCIPoll, "ci-poll", "P1-E25-W5-S51-T2", true, false},
		{EgressClassBridge, "bridge", "P1-E23-W5-S48-T1", true, false},
		{EgressClassNselfBackend, "nself-backend", "P1-E25-W5-S52-T2", true, false},
		{EgressClassWikiGitPush, "wiki-git-push", "P1-E25-W5-S51-T6", true, false},
		{EgressClassRecallWhat, "recall-what", "P1-E22-W5-S47-T1", true, false},
		{EgressClassConversationResume, "conversation-resume", "P1-E22-W5-S47-T2", true, true},
		{EgressClassGitHubPRDiff, "github-pr-diff", "P1-PLG-05", true, false},
		{EgressClassCIOIDCJWKS, "ci-oidc-jwks", "P1-CI-14", true, false},
		{EgressClassAgentDriver, "agent-driver", "P1-AGT-03", true, false},
		{EgressClassProviderUsage, "provider-usage", "P1-GWY-04", true, false},
		{EgressClassFleetDiscovery, "fleet-discovery", "P1-TOP-18", true, false},
		{EgressClassExternalExecutor, "external-executor", "P1-NODE-09", true, false},
	}
	registry := DefaultRegistry()
	if got, want := len(registry.Classes()), len(cases); got != want {
		t.Fatalf("the default registry holds %d classes, want %d: %v", got, want, registry.Classes())
	}
	for _, tc := range cases {
		if string(tc.class) != tc.id {
			t.Errorf("class constant = %q, want %q", string(tc.class), tc.id)
		}
		cfg, ok := registry.Lookup(tc.class)
		if !ok {
			t.Errorf("class %q is not registered", tc.id)
			continue
		}
		if cfg.Owner != tc.owner || cfg.Enabled != tc.enabled || cfg.AllowRestricted != tc.allowRestricted {
			t.Errorf("class %q config = %+v, want owner %q, enabled %v, allowRestricted %v", tc.id, cfg, tc.owner, tc.enabled, tc.allowRestricted)
		}
		if cfg.AllowLocalOnly && tc.class != EgressClassConversationResume { // resume admits all tiers: TestEgressClassConversationResumeAdmitsAllTiers

			t.Errorf("class %q admits local-only content; no shipped class does", tc.id)
		}
	}
}

// TestReleaseDownloadClassDeclaredHere pins the release-download constant
// and its registrant convention: declared in this package, registered only
// by internal/update's init, so this package's own registry never holds it.
func TestReleaseDownloadClassDeclaredHere(t *testing.T) {
	if string(EgressClassReleaseDownload) != "release-download" {
		t.Fatalf("EgressClassReleaseDownload = %q, want %q", string(EgressClassReleaseDownload), "release-download")
	}
	if _, ok := DefaultRegistry().Lookup(EgressClassReleaseDownload); ok {
		t.Fatal("the egress package registered release-download itself; internal/update owns the registration")
	}
	if !strings.Contains(readSourceFile(t, "classes.go"), `EgressClassReleaseDownload EgressClass = "release-download"`) {
		t.Fatal("classes.go does not declare EgressClassReleaseDownload as a literal EgressClass constant")
	}
}

// TestDisabledClassesEgressNothing drives the two disabled classes
// through the real entry point and asserts a refusal with zero output.
func TestDisabledClassesEgressNothing(t *testing.T) {
	engine := newTestEngine(t, nil)
	for _, class := range []EgressClass{EgressClassTelemetry, EgressClassPluginRemote} {
		out, err := engine.InterceptClass(context.Background(), class, TierPublic, []byte("payload"))
		if !errors.Is(err, ErrClassDisabled) {
			t.Errorf("InterceptClass(%q) = %v, want ErrClassDisabled", string(class), err)
		}
		if out != nil {
			t.Errorf("InterceptClass(%q) returned %d bytes; a disabled class egresses nothing", string(class), len(out))
		}
	}
}

// TestRegistrantListByteEqual asserts the package godoc's owner list is
// byte-equal to the forge spec's rule-17 owner list. The spec side is
// written out here verbatim; when either side moves, this fails.
func TestRegistrantListByteEqual(t *testing.T) {
	const specList = "K/S-22.T3 (conductor) · O/S-31.T3 (process plugin) · Q/S-37.T2 (node dispatch) · " +
		"Q/S-38.T1 (sync engine) · W/S-48.T1 (bridge registration) · X/S-50.T1 (registry fetch) · " +
		"Y/S-51.T2 (ci-poll) · Y/S-51.T6 (wiki-git-push) · AD/S-61.T2-T5 (agent-driver) · " +
		"AJ/S-72.T4 (external-executor) · W/S-49.T3 (bridge, messaging instance) · " +
		"Y/S-52.T2 (backend-integration) · AF/S-66.T2 (ci-mirror-push) · J/S-20.T1 (provider-intake) · " +
		"F/S-12.T5 (spike-measurement) · S/S-41.T3 (backup-target) · O/S-33.T4 (plugin-remote) · " +
		"X/S-50.T1 (registry-fetch, the fetch living in internal/plugins/registryfetch) · " +
		"AN/S-77.T5 (fleet-discovery) · P1-PLG-05 (github-pr-diff) · P1-PLG-08 (plugin-host-http) · P1-AGT-03 (agent-driver)"

	got := godocOwnerList(t)
	if got != specList {
		t.Fatalf("the package godoc owner list and the rule-17 list differ.\ngodoc: %q\nspec:  %q", got, specList)
	}
}

// godocOwnerList extracts the single owner-list line from doc.go.
func godocOwnerList(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(readSourceFile(t, "doc.go"), "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))
		if strings.HasPrefix(trimmed, "K/S-22.T3 (conductor)") {
			return trimmed
		}
	}
	t.Fatal("doc.go carries no owner list line starting with K/S-22.T3 (conductor)")
	return ""
}

// TestEgressClassConversationResumeAdmitsAllTiers pins the resume class's
// rule: it admits all four tiers, because where resumed content may go is
// decided per request before the class is reached (the source-versus-target
// rule in internal/conversation), and the class's own job is unconditional
// vault substitution. Each tier is driven through the real entry point.
func TestEgressClassConversationResumeAdmitsAllTiers(t *testing.T) {
	cfg, ok := DefaultRegistry().Lookup(EgressClassConversationResume)
	if !ok || !cfg.Enabled || !cfg.AllowRestricted || !cfg.AllowLocalOnly || len(cfg.AllowedTiers) != 0 {
		t.Fatalf("conversation-resume config = %+v (registered %v); want enabled, restricted+local-only admitted, no narrowing list", cfg, ok)
	}
	engine := newTestEngine(t, nil)
	for _, tier := range []SensitivityTier{TierLocalOnly, TierRestricted, TierInternal, TierPublic} {
		out, err := engine.InterceptClass(context.Background(), EgressClassConversationResume, tier, []byte("payload"))
		if err != nil || string(out) != "payload" {
			t.Errorf("tier %s: InterceptClass = %q, %v; want the payload admitted", tier, out, err)
		}
	}
	if _, err := engine.InterceptClass(context.Background(), EgressClassConversationResume, SensitivityTier(99), []byte("x")); !errors.Is(err, ErrSensitivityViolation) {
		t.Errorf("out-of-range tier = %v, want ErrSensitivityViolation (the range guard still holds)", err)
	}
}

// TestEgressClassGitHubPRDiffRegistered pins the dedicated review fetch policy.
func TestEgressClassGitHubPRDiffRegistered(t *testing.T) {
	cfg, ok := DefaultRegistry().Lookup(EgressClassGitHubPRDiff)
	if string(EgressClassGitHubPRDiff) != "github-pr-diff" || !ok || !cfg.Enabled || cfg.Owner != "P1-PLG-05" {
		t.Fatalf("github-pr-diff config = %+v, registered %v", cfg, ok)
	}
	if cfg.AllowRestricted || cfg.AllowLocalOnly || len(cfg.AllowedTiers) != 2 ||
		cfg.AllowedTiers[0] != TierInternal || cfg.AllowedTiers[1] != TierPublic {
		t.Fatalf("github-pr-diff must admit only internal and public: %+v", cfg)
	}
	engine := newTestEngine(t, nil)
	for _, tier := range []SensitivityTier{TierInternal, TierPublic} {
		out, err := engine.InterceptClass(context.Background(), EgressClassGitHubPRDiff, tier, []byte("payload"))
		if err != nil || string(out) != "payload" {
			t.Errorf("tier %s: InterceptClass = %q, %v", tier, out, err)
		}
	}
}

// TestEgressClassGitHubPRDiffRefusesLocalOnlyAndRestricted proves refusal emits nothing.
func TestEgressClassGitHubPRDiffRefusesLocalOnlyAndRestricted(t *testing.T) {
	engine := newTestEngine(t, nil)
	for _, tier := range []SensitivityTier{TierLocalOnly, TierRestricted} {
		out, err := engine.InterceptClass(context.Background(), EgressClassGitHubPRDiff, tier, []byte("payload"))
		if !errors.Is(err, ErrSensitivityViolation) || out != nil {
			t.Errorf("tier %s: InterceptClass = %q, %v; want no bytes and sensitivity refusal", tier, out, err)
		}
	}
}

// TestEgressClassPluginHostHTTPRegistered pins the host HTTP firewall policy.
func TestEgressClassPluginHostHTTPRegistered(t *testing.T) {
	cfg, ok := DefaultRegistry().Lookup(EgressClassPluginHostHTTP)
	if !ok || string(EgressClassPluginHostHTTP) != "plugin-host-http" || !cfg.Enabled || cfg.Owner != "P1-PLG-08" || cfg.AllowRestricted || cfg.AllowLocalOnly {
		t.Fatalf("host HTTP class = %+v, registered=%v", cfg, ok)
	}
	if len(cfg.AllowedTiers) != 2 || cfg.AllowedTiers[0] != TierInternal || cfg.AllowedTiers[1] != TierPublic {
		t.Fatalf("host HTTP tiers = %v", cfg.AllowedTiers)
	}
	engine := newTestEngine(t, nil)
	for _, tier := range []SensitivityTier{TierLocalOnly, TierRestricted} {
		if out, err := engine.InterceptClass(context.Background(), EgressClassPluginHostHTTP, tier, []byte("payload")); err == nil || out != nil {
			t.Fatalf("host HTTP admitted %s", tier)
		}
	}
}

// TestCIOIDCJWKSClassPublicOnly asserts the GitHub Actions OIDC JWKS class
// admits only the public tier and refuses restricted/internal/local-only.
func TestCIOIDCJWKSClassPublicOnly(t *testing.T) {
	cfg, ok := DefaultRegistry().Lookup(EgressClassCIOIDCJWKS)
	if !ok || string(EgressClassCIOIDCJWKS) != "ci-oidc-jwks" || !cfg.Enabled || cfg.AllowRestricted || cfg.AllowLocalOnly || cfg.Owner != "P1-CI-14" {
		t.Fatalf("ci-oidc-jwks config = %+v, registered=%v", cfg, ok)
	}
	if len(cfg.AllowedTiers) != 1 || cfg.AllowedTiers[0] != TierPublic {
		t.Fatalf("ci-oidc-jwks must admit only public tier: %+v", cfg.AllowedTiers)
	}
	engine := newTestEngine(t, nil)
	out, err := engine.InterceptClass(context.Background(), EgressClassCIOIDCJWKS, TierPublic, []byte("jwks"))
	if err != nil || string(out) != "jwks" {
		t.Fatalf("public tier refused: %q, %v", out, err)
	}
	for _, tier := range []SensitivityTier{TierLocalOnly, TierRestricted, TierInternal} {
		out, err := engine.InterceptClass(context.Background(), EgressClassCIOIDCJWKS, tier, []byte("jwks"))
		if !errors.Is(err, ErrSensitivityViolation) || out != nil {
			t.Errorf("tier %s: got %q, %v; want sensitivity refusal", tier, out, err)
		}
	}
}

// TestAgentDriverEgressClassRegistered: exactly one agent-driver row, enabled,
// refusing restricted content, and part of the inventory the default registry
// reports at daemon start.
func TestAgentDriverEgressClassRegistered(t *testing.T) {
	count := 0
	for _, row := range defaultClasses {
		if row.Class == EgressClassAgentDriver {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("defaultClasses holds %d agent-driver rows, want exactly 1", count)
	}
	cfg, ok := DefaultRegistry().Lookup(EgressClassAgentDriver)
	if !ok || !cfg.Enabled || cfg.AllowRestricted || cfg.AllowLocalOnly || cfg.Owner != "P1-AGT-03" {
		t.Fatalf("agent-driver config = %+v (registered %v), want enabled, no restricted, owner P1-AGT-03", cfg, ok)
	}
	found := false
	for _, c := range DefaultRegistry().Classes() {
		found = found || c == EgressClassAgentDriver
	}
	if !found {
		t.Fatalf("Classes() = %v lacks agent-driver", DefaultRegistry().Classes())
	}
	out, err := newTestEngine(t, nil).InterceptClass(context.Background(), EgressClassAgentDriver, TierRestricted, []byte("x"))
	if !errors.Is(err, ErrSensitivityViolation) || out != nil {
		t.Fatalf("restricted tier through agent-driver = %q, %v; want ErrSensitivityViolation", out, err)
	}
}

// TestDefaultClassesExternalExecutor asserts the external-executor class
// is registered exactly once with public-only tiers and the P1-NODE-09 owner.
func TestDefaultClassesExternalExecutor(t *testing.T) {
	count := 0
	for _, row := range defaultClasses {
		if row.Class == EgressClassExternalExecutor {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("defaultClasses holds %d external-executor rows, want exactly 1", count)
	}
	cfg, ok := DefaultRegistry().Lookup(EgressClassExternalExecutor)
	if !ok || !cfg.Enabled || cfg.AllowRestricted || cfg.AllowLocalOnly || cfg.Owner != "P1-NODE-09" {
		t.Fatalf("external-executor config = %+v (registered %v), want enabled, no restricted, owner P1-NODE-09", cfg, ok)
	}
	if len(cfg.AllowedTiers) != 1 || cfg.AllowedTiers[0] != TierPublic {
		t.Fatalf("external-executor must admit only public tier: %+v", cfg.AllowedTiers)
	}
	engine := newTestEngine(t, nil)
	out, err := engine.InterceptClass(context.Background(), EgressClassExternalExecutor, TierPublic, []byte("payload"))
	if err != nil || string(out) != "payload" {
		t.Fatalf("public tier refused: %q, %v", out, err)
	}
	for _, tier := range []SensitivityTier{TierLocalOnly, TierRestricted, TierInternal} {
		out, err := engine.InterceptClass(context.Background(), EgressClassExternalExecutor, tier, []byte("payload"))
		if !errors.Is(err, ErrSensitivityViolation) || out != nil {
			t.Errorf("tier %s: got %q, %v; want sensitivity refusal", tier, out, err)
		}
	}
}
