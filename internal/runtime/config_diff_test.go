package runtime

// Purpose: tests for ApplyDiff (config_diff.go) — the all-or-nothing,
//   plugin-owned diff-apply seam behind `cascade nself handshake`.
// Inputs: n/a (test-only, every write under t.TempDir()).
// Outputs: n/a (test-only).
// Constraints: no network, no real clock; every case is hermetic.
// SPORT: internal/runtime config_diff.go (ADD) — P1-E25-W5-S103-T1.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func writerAt(t *testing.T, contents string) *ConfigWriter {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.toml")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("seed config: %v", err)
		}
	}
	return &ConfigWriter{Path: path}
}

func TestApplyDiffAllOrNothing(t *testing.T) {
	w := writerAt(t, "")
	diff := ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
		{Path: "plugins.cascade-nself.project_dir", Literal: `"/proj"`},
		{Path: "elevation.allow_remote", Literal: "true"}, // guarded: refuses the whole batch
	}}
	_, err := w.ApplyDiff(diff)
	assertKind(t, err, cascade.KindPolicyDenied)
	data, err := os.ReadFile(w.Path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("unexpected read error: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("expected nothing written, got %q", data)
	}
}

func TestApplyDiffIdempotentNoOp(t *testing.T) {
	w := writerAt(t, "")
	diff := ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
		{Path: "plugins.cascade-nself.project_dir", Literal: `"/proj"`},
		{Path: "runtime.profile", Literal: `"server"`},
	}}
	first, err := w.ApplyDiff(diff)
	if err != nil {
		t.Fatalf("first ApplyDiff: %v", err)
	}
	if len(first.Applied) != 2 {
		t.Fatalf("expected 2 applied, got %d (%+v)", len(first.Applied), first)
	}
	afterFirst, _ := os.ReadFile(w.Path)

	second, err := w.ApplyDiff(diff)
	if err != nil {
		t.Fatalf("second ApplyDiff: %v", err)
	}
	if len(second.Applied) != 0 || len(second.Unchanged) != 2 {
		t.Fatalf("second run not a no-op: %+v", second)
	}
	afterSecond, _ := os.ReadFile(w.Path)
	if string(afterFirst) != string(afterSecond) {
		t.Fatalf("second run rewrote the file:\nfirst:  %q\nsecond: %q", afterFirst, afterSecond)
	}
}

func TestApplyDiffSkipsUserSetKey(t *testing.T) {
	w := writerAt(t, "[runtime]\nprofile = \"worker\"\n")
	diff := ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
		{Path: "runtime.profile", Literal: `"server"`},
	}}
	result, err := w.ApplyDiff(diff)
	if err != nil {
		t.Fatalf("ApplyDiff: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Reason != "user-set" {
		t.Fatalf("expected one user-set skip, got %+v", result)
	}
	if len(result.Applied) != 0 {
		t.Fatalf("expected nothing applied, got %+v", result.Applied)
	}
	data, _ := os.ReadFile(w.Path)
	if string(data) != "[runtime]\nprofile = \"worker\"\n" {
		t.Fatalf("file mutated: %q", data)
	}
}

func TestApplyDiffRecordsManaged(t *testing.T) {
	w := writerAt(t, "")
	diff := ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
		{Path: "runtime.profile", Literal: `"server"`},
		{Path: "plugins.cascade-nself.a.b", Literal: "1"},
		{Path: "plugins.cascade-nself.a__b", Literal: "2"},
	}}
	if _, err := w.ApplyDiff(diff); err != nil {
		t.Fatalf("ApplyDiff: %v", err)
	}
	data, err := os.ReadFile(w.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The contract's shape: one inline table keyed by the full dotted path,
	// value = the canonical literal. a.b and a__b are two records.
	want := `managed = {"plugins.cascade-nself.a.b" = "1", "plugins.cascade-nself.a__b" = "2", "runtime.profile" = "\"server\""}`
	if !strings.Contains(string(data), want) {
		t.Fatalf("config.toml lacks the managed record %s:\n%s", want, data)
	}

	// A later diff changing a value the owner recorded is Applied, not
	// skipped as user-set, because the current value equals the record.
	second := ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
		{Path: "runtime.profile", Literal: `"worker"`},
	}}
	result, err := w.ApplyDiff(second)
	if err != nil {
		t.Fatalf("second ApplyDiff: %v", err)
	}
	if len(result.Applied) != 1 || result.Applied[0].Reason != "owner-managed" {
		t.Fatalf("expected owner-managed apply, got %+v", result)
	}
}

func TestApplyDiffRefusesGuardedSections(t *testing.T) {
	cases := []struct {
		family string
		path   string
		lit    string
	}{
		{"elevation", "elevation.allow_remote", "true"},
		{"policy", "policy.autonomy_profile", `"open"`},
		{"secrets", "secrets.keychain_backend", `"plaintext"`},
		{"sync", "sync.class", `"server-primary"`},
		{"nodes", "nodes.trust_tier", `"controller"`},
		{"conductor", "conductor.external_routing_enabled", "true"},
		{"agents", "agents.egress.allowlist", `["*"]`},
	}
	for _, tc := range cases {
		t.Run(tc.family, func(t *testing.T) {
			w := writerAt(t, guardSeed)
			diff := ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{{Path: tc.path, Literal: tc.lit}}}
			_, err := w.ApplyDiff(diff)
			assertKind(t, err, cascade.KindPolicyDenied)
			assertUnchanged(t, w)
		})
	}
}

func TestEffectiveOfTreeMatchesLoad(t *testing.T) {
	toml := "[elevation]\nallow_remote = true\nhelper_pubkey = \"pk\"\n\n" +
		"[policy]\nautonomy_profile = \"supervised\"\n\n" +
		"[secrets]\nkeychain_backend = \"os\"\n\n" +
		"[nodes]\ntrust_tier = \"worker-trusted\"\n\n" +
		"[conductor]\nexternal_routing_enabled = false\nspill_enabled = true\n\n" +
		"[sync]\nmemory = \"synced\"\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantEff := extractEffectiveConfig(cfg)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	tree, err := decodeForValidate(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	gotEff, err := effectiveOfTree(tree)
	if err != nil {
		t.Fatalf("effectiveOfTree: %v", err)
	}

	if !reflect.DeepEqual(gotEff, wantEff) {
		t.Fatalf("effectiveOfTree diverged from Load:\ngot:  %+v\nwant: %+v", gotEff, wantEff)
	}
}
