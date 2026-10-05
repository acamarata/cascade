package learn

// Purpose: the hard denylist query (C-p1-requirements item 28), table-driven
//   over every rule of testdata/denylist_golden.json so deleting or
//   reordering any rule fails.
// SPORT: learn/denylist_test (P1-LRN-01).

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The two invalid-path messages of validateConcretePath.
const (
	msgEmptyPath   = "learn: config path is empty"
	msgBadSegment  = "learn: config path must be dot-separated lowercase bare-key segments"
	msgEmptyQuery  = "learn: denylist query needs at least one path"
	denyGoldenPath = "testdata/denylist_golden.json"
)

// goldenRule is one row of testdata/denylist_golden.json.
type goldenRule struct {
	Index   int    `json:"index"`
	Area    string `json:"area"`
	Matcher string `json:"matcher"`
	Example string `json:"example"`
}

func loadDenyGolden(t *testing.T) []goldenRule {
	t.Helper()
	raw, err := os.ReadFile(denyGoldenPath)
	if err != nil {
		t.Fatalf("read deny golden: %v", err)
	}
	var g struct {
		Rules []goldenRule `json:"rules"`
	}
	if err := json.Unmarshal(raw, &g); err != nil || len(g.Rules) != 44 {
		t.Fatalf("parse deny golden (%d rules, want 44): %v", len(g.Rules), err)
	}
	return g.Rules
}

func TestDenyRulesQuery(t *testing.T) {
	isolateHome(t)
	golden := loadDenyGolden(t)
	rules := DenyRules()
	if len(rules) != len(golden) {
		t.Fatalf("DenyRules() has %d rules, golden %d", len(rules), len(golden))
	}
	perArea := map[DenylistArea]int{}
	for i, g := range golden {
		r := rules[i]
		if r.Index != i || g.Index != i || string(r.Area) != g.Area || string(r.Matcher) != g.Matcher {
			t.Fatalf("rule %d = %+v, golden %+v", i, r, g)
		}
		perArea[r.Area]++
		got, err := MatchDenylist([]FieldPath{FieldPath(g.Example)})
		if err != nil || len(got) != 1 || got[0] != r {
			t.Errorf("MatchDenylist(%q) = (%+v, %v), want rule %d", g.Example, got, err, i)
		}
	}
	for _, a := range DenylistAreas() {
		if perArea[a] == 0 {
			t.Errorf("area %s has no deny rule", a)
		}
	}
	assertNamedDenyMatches(t)
	assertDenyQueryRefusals(t)
}

// assertNamedDenyMatches checks the (index, area) acceptance[4] names, and
// that a learnable key returns no rule and a nil error.
func assertNamedDenyMatches(t *testing.T) {
	t.Helper()
	want := map[FieldPath]int{
		"policy.autonomy_profile": 1, "grants.ci-mirror": 5, "secrets.keychain_backend": 8,
		"elevation.helper_pubkey": 10, "jobs.gates.high": 14, "ci.policy.required": 16,
		"ci.requirements.kinds": 17, "ci.attestation.high": 18, "review.min_reviewers": 19,
		"providers.compliance.flag-a": 21, "providers.capabilities.authoring": 22,
		"agents.egress.harness-x.class": 25, "ci.mirror_remote": 26, "gateways.acct-a.base_url": 27,
		"gateways.acct-a.credential_helper": 32, "agents.auth.harness-x.gateway": 33,
		"ci.toolchain_versions": 34, "nodes.repos.repo-a": 35, "fabric.class.trusted": 36,
		"conductor.retry.max_attempts": 39, "backup.retention_days": 43,
		"gateways.acct-a": 27, "policy": 0, "ci": 16,
	}
	rules := DenyRules()
	paths := make([]FieldPath, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	got, err := MatchDenylist(paths)
	if err != nil || len(got) != len(paths) {
		t.Fatalf("MatchDenylist(named) = (%d rules, %v), want %d", len(got), err, len(paths))
	}
	for i, p := range paths {
		if r := rules[want[p]]; got[i] != r {
			t.Errorf("MatchDenylist(%q) = %+v, want %+v", p, got[i], r)
		}
	}
	got, err = MatchDenylist([]FieldPath{"retrieval.fusion.k", "secrets.x"})
	if err != nil || len(got) != 1 || got[0].Index != 8 {
		t.Fatalf("mixed query = (%+v, %v), want only rule 8", got, err)
	}
	got, err = MatchDenylist([]FieldPath{"retrieval.fusion.k"})
	if err != nil || got != nil {
		t.Fatalf("MatchDenylist(retrieval.fusion.k) = (%+v, %v), want (nil, nil)", got, err)
	}
}

// assertDenyQueryRefusals: an unanswerable query refuses, never reads as
// "not denied".
func assertDenyQueryRefusals(t *testing.T) {
	t.Helper()
	got, err := MatchDenylist(nil)
	if got != nil {
		t.Fatal("empty query returned rules")
	}
	wantErr(t, err, cascade.KindInvalidInput, msgEmptyQuery)
	_, err = MatchDenylist([]FieldPath{})
	wantErr(t, err, cascade.KindInvalidInput, msgEmptyQuery)
	_, err = MatchDenylist([]FieldPath{""})
	wantErr(t, err, cascade.KindInvalidInput, msgEmptyPath)
	for _, bad := range []FieldPath{"a..b", " x", "Policy.auth.mode", "policy.*", "a.", ".a", "a b.c"} {
		got, err := MatchDenylist([]FieldPath{"secrets.x", bad})
		if got != nil {
			t.Fatalf("MatchDenylist with invalid %q returned partial rules %+v", bad, got)
		}
		wantErr(t, err, cascade.KindInvalidInput, msgBadSegment)
	}
}
