package egress

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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
		{EgressClassMCP, "mcp.response", "D/S-06.T6", true, false},
		{EgressClassHook, "hook.response", "C/S-05.T1", true, false},
		{EgressClassTelemetry, "telemetry", "P1-E08-W2-S16-T1", false, false},
		{EgressClassOAuth, "oauth", "H/S-15.T2", true, false},
		{EgressClassProviderIntake, "provider-intake", "J/S-20.T1", true, false},
		{EgressClassSpikeMeasurement, "spike-measurement", "F/S-12.T5", true, false},
		{EgressClassBackupTarget, "backup-target", "S/S-41.T3", true, true},
		{EgressClassPluginRemote, "plugin-remote", "O/S-33.T4", false, false},
		{EgressClassRegistryFetch, "registry-fetch", "X/S-50.T1", true, false},
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
		if cfg.Owner != tc.owner {
			t.Errorf("class %q owner = %q, want %q", tc.id, cfg.Owner, tc.owner)
		}
		if cfg.Enabled != tc.enabled {
			t.Errorf("class %q enabled = %v, want %v", tc.id, cfg.Enabled, tc.enabled)
		}
		if cfg.AllowRestricted != tc.allowRestricted {
			t.Errorf("class %q allowRestricted = %v, want %v", tc.id, cfg.AllowRestricted, tc.allowRestricted)
		}
		if cfg.AllowLocalOnly {
			t.Errorf("class %q admits local-only content; no shipped class does", tc.id)
		}
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
		"AN/S-77.T5 (fleet-discovery)"

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
