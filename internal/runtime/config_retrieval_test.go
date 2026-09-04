package runtime

import (
	"context"
	"errors"
	"testing"
)

// Purpose: config_retrieval.go's tests — the default, the explicit
//   values, the fail-closed rejections, the env override, and the
//   guarantee that S-12.T4's unowned [retrieval] keys survive untouched.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7.1 — every case writes its config.toml under
//   t.TempDir() and injects Getenv/Environ; nothing reads the real
//   process environment or $HOME.
// SPORT: runtime/config.retrieval (ADD, P1-E06-W2-S12-T3).

// loadRetrieval loads a config.toml holding body and returns it.
func loadRetrieval(t *testing.T, body string, env map[string]string) (*Config, error) {
	t.Helper()
	path := writeConfigFile(t, t.TempDir(), body)
	return Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(env),
	})
}

// TestRerankerEnabledDefaultsToFalse pins the shipped default across the
// three ways of not asking for reranking: no file content at all, a
// [retrieval] table without the sub-table, and an explicit false.
func TestRerankerEnabledDefaultsToFalse(t *testing.T) {
	for name, body := range map[string]string{
		"no retrieval section": "[runtime]\nprofile = \"local\"\n",
		"no reranker table":    "[retrieval]\nsources = [\"notes\"]\n",
		"explicitly false":     "[retrieval.reranker]\nenabled = false\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadRetrieval(t, body, nil)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.RerankerEnabled() {
				t.Error("retrieval.reranker.enabled resolved true, want false")
			}
		})
	}
}

// TestRerankerEnabledTrue is the positive case, in the exact dotted table
// form 08 §3 defines (R-14.18 — there is no reranker_enabled variant).
func TestRerankerEnabledTrue(t *testing.T) {
	cfg, err := loadRetrieval(t, "[retrieval.reranker]\nenabled = true\n", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.RerankerEnabled() {
		t.Error("retrieval.reranker.enabled resolved false, want true")
	}
}

// TestRerankerEnabledEnvOverride: the generic CASCADE_<SECTION>__<KEY>
// machinery reaches the nested key and yields a real boolean.
func TestRerankerEnabledEnvOverride(t *testing.T) {
	cfg, err := loadRetrieval(t, "[retrieval.reranker]\nenabled = false\n",
		map[string]string{"CASCADE_RETRIEVAL__RERANKER__ENABLED": "true"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.RerankerEnabled() {
		t.Error("env override did not enable the stage")
	}
}

// TestRerankerConfigFailsClosed: every malformed way of asking for
// reranking is a hard typed error. A user who wrote one of these asked
// for a reranker; loading successfully with the stage quietly off would
// tell them it is running when it is not.
func TestRerankerConfigFailsClosed(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"non-boolean value":   {"[retrieval.reranker]\nenabled = \"yes\"\n", "retrieval.reranker.enabled"},
		"misspelled key":      {"[retrieval.reranker]\nenable = true\n", "retrieval.reranker.enable"},
		"reranker not table":  {"[retrieval]\nreranker = true\n", "retrieval.reranker"},
		"retrieval not table": {"retrieval = 1\n", "retrieval"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadRetrieval(t, tc.body, nil)
			if err == nil {
				t.Fatal("malformed reranker config loaded successfully")
			}
			var cfgErr *ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("err = %v, want a *ConfigError", err)
			}
			if cfgErr.Field != tc.field {
				t.Errorf("Field = %q, want %q", cfgErr.Field, tc.field)
			}
		})
	}
}

// TestRetrievalUnownedKeysPreserved: this ticket owns
// [retrieval.reranker] and nothing else under [retrieval]. S-12.T4's keys
// must survive in Extra, unvalidated and unwarned, exactly as every other
// unowned section does.
func TestRetrievalUnownedKeysPreserved(t *testing.T) {
	cfg, err := loadRetrieval(t, "[retrieval]\nsources = [\"notes\"]\n\n[retrieval.fusion]\nk = 60\n\n[retrieval.reranker]\nenabled = true\n", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	retrieval, ok := cfg.Extra["retrieval"].(map[string]interface{})
	if !ok {
		t.Fatalf("[retrieval] missing from Extra: %#v", cfg.Extra)
	}
	fusion, ok := retrieval["fusion"].(map[string]interface{})
	if !ok {
		t.Fatalf("[retrieval.fusion] not preserved: %#v", retrieval)
	}
	if fusion["k"] != int64(60) {
		t.Errorf("retrieval.fusion.k = %#v, want 60", fusion["k"])
	}
	if _, ok := retrieval["sources"]; !ok {
		t.Error("retrieval.sources was dropped")
	}
}

// TestRerankerEnabledNilConfig: the accessor is safe on a nil *Config and
// reports the restrictive answer, so a caller that failed to load cannot
// accidentally run an unconfigured stage.
func TestRerankerEnabledNilConfig(t *testing.T) {
	var cfg *Config
	if cfg.RerankerEnabled() {
		t.Error("nil config reported the reranker enabled")
	}
}
