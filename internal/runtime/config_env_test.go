package runtime

import (
	"context"
	"testing"
)

// Purpose: tests for the generic CASCADE_<SECTION>__<KEY> env-override
//   machinery in config_env.go (collectEnvOverrides, parseEnvLiteral,
//   treeSet, reservedEnvVars), exercised through the public Load entry
//   point, plus profile-flag precedence through Load. Split out of a
//   single config_test.go per R-14.117 (Art.10.3 file-cap remedy) —
//   behaviour-preserving, moved code only.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7.1 — every test uses t.TempDir() and injected
//   Getenv/Environ, never the real process environment.
// SPORT: runtime/config (ADD, placeholder per T-1 sport_updates).

func TestLoad_GenericEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[retrieval]\n[retrieval.fusion]\nk = 60\n")
	cfg, err := Load(context.Background(), LoadOptions{
		Path:   path,
		Getenv: func(string) string { return "" },
		Environ: fakeEnviron(map[string]string{
			"CASCADE_RETRIEVAL__FUSION__K": "80",
		}),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	retrieval, ok := cfg.Extra["retrieval"].(map[string]interface{})
	if !ok {
		t.Fatalf("Extra[retrieval] = %#v", cfg.Extra["retrieval"])
	}
	fusion, ok := retrieval["fusion"].(map[string]interface{})
	if !ok {
		t.Fatalf("retrieval.fusion = %#v", retrieval["fusion"])
	}
	if fusion["k"] != int64(80) {
		t.Errorf("retrieval.fusion.k = %v (%T), want int64(80)", fusion["k"], fusion["k"])
	}
	if cfg.Source("retrieval.fusion.k") != SourceEnv {
		t.Errorf("Source(retrieval.fusion.k) = %q, want env", cfg.Source("retrieval.fusion.k"))
	}
}

func TestLoad_ReservedEnvVarsNeverTreatedAsOverrides(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "")
	cfg, err := Load(context.Background(), LoadOptions{
		Path:   path,
		Getenv: func(string) string { return "" },
		Environ: fakeEnviron(map[string]string{
			"CASCADE_HOME":       "/somewhere",
			"CASCADE_INIT_TOKEN": "secret",
		}),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Extra["home"]; ok {
		t.Error("CASCADE_HOME leaked into Extra as a generic override")
	}
	if _, ok := cfg.Extra["init"]; ok {
		t.Error("CASCADE_INIT_TOKEN leaked into Extra as a generic override")
	}
}

func TestLoad_ProfileFlagPrecedenceThroughLoad(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[runtime]\nprofile = \"local\"\n")
	cfg, err := Load(context.Background(), LoadOptions{
		Path:        path,
		ProfileFlag: "server",
		Getenv: func(k string) string {
			if k == "CASCADE_PROFILE" {
				return "worker"
			}
			return ""
		},
		Environ: fakeEnviron(nil),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Profile != ProfileServer {
		t.Errorf("Profile = %q, want server (flag beats env and file)", cfg.Runtime.Profile)
	}
	if cfg.Source("runtime.profile") != SourceFlag {
		t.Errorf("Source(runtime.profile) = %q, want flag", cfg.Source("runtime.profile"))
	}
}
