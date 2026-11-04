package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestHostedWorkflowFromGuardedFamily exercises registration and baseline admission.
func TestHostedWorkflowFromGuardedFamily(t *testing.T) {
	for _, key := range []string{"ci.hosted.allow", "ci.hosted.workflow"} {
		if _, err := runtime.ResolveDottedPath(key); err != nil {
			t.Fatalf("guarded key %s: %v", key, err)
		}
	}
	base, _ := loadBody(t, "")
	allowed, _ := loadBody(t, "[ci.hosted]\nallow = true\nworkflow = \"custom.yml\"\n")
	paths := pathKeys(runtime.CompareSecurity(runtime.EffectiveConfigOf(base), runtime.EffectiveConfigOf(allowed)))
	for _, key := range []string{"ci.hosted.allow", "ci.hosted.workflow"} {
		if !slices.Contains(paths, key) {
			t.Fatalf("missing loosening %s in %v", key, paths)
		}
	}
	res, cfg, _ := restart(t, base, allowed, runtime.BaselineFile{})
	if !res.Outcome.Degraded() {
		t.Fatal("unadmitted hosted config was accepted")
	}
	raw, err := extractCIHostedFamily(cfg.Extra)
	if err != nil || len(raw) != 0 {
		t.Fatalf("fallback must restore defaults: %s, %v", raw, err)
	}
	if out := reload(t, "", "[ci.hosted]\nallow = true\n"); !out.ElevationRequired {
		t.Fatal("allow without admission did not require elevation")
	}
	if out := reload(t, "[ci.hosted]\nallow = true\n", ""); !out.Accepted {
		t.Fatal("disabling hosted execution must tighten")
	}
}

// TestHostedFamilyValidationAndComparison checks malformed values and workflow changes.
func TestHostedFamilyValidationAndComparison(t *testing.T) {
	for _, workflow := range []interface{}{"", "../ci.yml", ".github/workflows/ci.yml", "a/b.yml", "a\\b.yml", "https://example.test/ci.yml", 42} {
		_, err := extractCIHostedFamily(map[string]interface{}{"ci": map[string]interface{}{"hosted": map[string]interface{}{"workflow": workflow}}})
		if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
			t.Errorf("workflow %v: %v", workflow, err)
		}
	}
	for _, extra := range []map[string]interface{}{
		{"ci": false}, {"ci": map[string]interface{}{"hosted": false}},
		{"ci": map[string]interface{}{"hosted": map[string]interface{}{"allow": "true"}}},
	} {
		_, err := extractCIHostedFamily(extra)
		if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
			t.Errorf("malformed config accepted: %v", extra)
		}
	}
	paths, err := compareCIHostedFamily(json.RawMessage(`{"workflow":"custom.yml"}`), nil)
	if err != nil || !slices.Contains(pathKeys(paths), "ci.hosted.workflow") {
		t.Fatalf("restoring default workflow must loosen: %v, %v", paths, err)
	}
}

// TestHostedFamilyApplyPreservesSiblings verifies fallback copies the CI table.
func TestHostedFamilyApplyPreservesSiblings(t *testing.T) {
	ci := map[string]interface{}{"local": "preserved", "hosted": map[string]interface{}{"allow": true}}
	extra := map[string]interface{}{"ci": ci}
	applyCIHostedFamily(extra, nil)
	if ci["hosted"].(map[string]interface{})["allow"] != true {
		t.Fatal("apply mutated the original table")
	}
	if extra["ci"].(map[string]interface{})["local"] != "preserved" {
		t.Fatal("apply removed a sibling CI section")
	}
	if raw, err := extractCIHostedFamily(extra); err != nil || len(raw) != 0 {
		t.Fatalf("fallback: %s, %v", raw, err)
	}
}
