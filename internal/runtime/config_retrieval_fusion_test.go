package runtime

import (
	"errors"
	"math"
	"testing"
)

// Purpose: config_retrieval_fusion.go's tests — the shipped defaults, the
//   accepted forms, every fail-closed rejection, and the copy-on-read
//   guarantee of the accessors.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7.1 — every case writes its config.toml under
//   t.TempDir() and injects Getenv/Environ; nothing reads the real
//   process environment or $HOME.
// SPORT: runtime/config.retrieval (ADD, P1-E06-W2-S12-T4).

// TestRetrievalDefaults pins what an operator gets without writing any
// [retrieval] key: no sources, k = 60, no weights.
func TestRetrievalDefaults(t *testing.T) {
	for name, body := range map[string]string{
		"no file content":      "",
		"no retrieval section": "[runtime]\nprofile = \"local\"\n",
		"empty retrieval":      "[retrieval]\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadRetrieval(t, body, nil)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.RetrievalSources(); got != nil {
				t.Errorf("RetrievalSources() = %v, want nil", got)
			}
			if got := cfg.FusionK(); got != DefaultFusionK {
				t.Errorf("FusionK() = %d, want %d", got, DefaultFusionK)
			}
			if got := cfg.FusionWeights(); got != nil {
				t.Errorf("FusionWeights() = %v, want nil", got)
			}
		})
	}
}

// TestRetrievalConfiguredValues reads back the whole surface as written.
func TestRetrievalConfiguredValues(t *testing.T) {
	cfg, err := loadRetrieval(t, "[retrieval]\nsources = [\"notes\", \"code\"]\n\n"+
		"[retrieval.fusion]\nk = 80\nweights = { fts5 = 2.5, vector = 1 }\n", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sources := cfg.RetrievalSources()
	if len(sources) != 2 || sources[0] != "notes" || sources[1] != "code" {
		t.Errorf("RetrievalSources() = %v, want [notes code] in file order", sources)
	}
	if got := cfg.FusionK(); got != 80 {
		t.Errorf("FusionK() = %d, want 80", got)
	}
	weights := cfg.FusionWeights()
	if weights["fts5"] != 2.5 {
		t.Errorf("weights[fts5] = %v, want 2.5", weights["fts5"])
	}
	// An integer literal widens: `vector = 1` is what an operator writes.
	if weights["vector"] != 1 {
		t.Errorf("weights[vector] = %v, want 1", weights["vector"])
	}
}

// TestRetrievalAccessorsCopy: a caller cannot reach into the loaded
// config through a returned slice or map.
func TestRetrievalAccessorsCopy(t *testing.T) {
	cfg, err := loadRetrieval(t, "[retrieval]\nsources = [\"notes\"]\n\n"+
		"[retrieval.fusion]\nweights = { fts5 = 1.0 }\n", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.RetrievalSources()[0] = "tampered"
	cfg.FusionWeights()["fts5"] = 99
	if cfg.RetrievalSources()[0] != "notes" {
		t.Error("mutating the returned slice changed the loaded config")
	}
	if cfg.FusionWeights()["fts5"] != 1.0 {
		t.Error("mutating the returned map changed the loaded config")
	}
}

// TestRetrievalNilConfigAccessors: the accessors are safe on a nil
// *Config and report the shipped defaults.
func TestRetrievalNilConfigAccessors(t *testing.T) {
	var cfg *Config
	if cfg.RetrievalSources() != nil || cfg.FusionWeights() != nil {
		t.Error("a nil config reported configured retrieval values")
	}
	if cfg.FusionK() != DefaultFusionK {
		t.Errorf("nil config FusionK() = %d, want %d", cfg.FusionK(), DefaultFusionK)
	}
}

// TestRetrievalFusionEnabledPassedOver: retrieval.fusion.enabled is
// F/S-12.T6's key end-to-end (R-16.49). This ticket must neither reject
// it as unknown nor claim it: it loads without error and survives in
// Extra for its owner to read.
func TestRetrievalFusionEnabledPassedOver(t *testing.T) {
	cfg, err := loadRetrieval(t, "[retrieval.fusion]\nenabled = true\nk = 60\n", nil)
	if err != nil {
		t.Fatalf("Load rejected S-12.T6's key: %v", err)
	}
	retrieval, ok := cfg.Extra["retrieval"].(map[string]interface{})
	if !ok {
		t.Fatalf("[retrieval] missing from Extra: %#v", cfg.Extra)
	}
	fusion, ok := retrieval["fusion"].(map[string]interface{})
	if !ok {
		t.Fatalf("[retrieval.fusion] not preserved: %#v", retrieval)
	}
	if fusion["enabled"] != true {
		t.Errorf("retrieval.fusion.enabled = %#v, want it preserved as true", fusion["enabled"])
	}
}

// TestRetrievalConfigFailsClosed: every malformed way of configuring
// retrieval is a hard typed error naming the offending field. An operator
// who wrote one of these tuned their retrieval; loading successfully on
// the defaults would tell them a tuning is live when it is not.
func TestRetrievalConfigFailsClosed(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"unknown retrieval key": {"[retrieval]\nchunk_size = 400\n", "retrieval.chunk_size"},
		"unknown fusion key":    {"[retrieval.fusion]\nrrf_k = 60\n", "retrieval.fusion.rrf_k"},
		"sources not an array":  {"[retrieval]\nsources = \"notes\"\n", "retrieval.sources"},
		"source not a string":   {"[retrieval]\nsources = [\"notes\", 7]\n", "retrieval.sources[1]"},
		"empty source":          {"[retrieval]\nsources = [\"\"]\n", "retrieval.sources[0]"},
		"duplicate source":      {"[retrieval]\nsources = [\"notes\", \"notes\"]\n", "retrieval.sources[1]"},
		"fusion not a table":    {"[retrieval]\nfusion = 60\n", "retrieval.fusion"},
		"k not an integer":      {"[retrieval.fusion]\nk = \"60\"\n", "retrieval.fusion.k"},
		"k is a float":          {"[retrieval.fusion]\nk = 60.5\n", "retrieval.fusion.k"},
		"k is zero":             {"[retrieval.fusion]\nk = 0\n", "retrieval.fusion.k"},
		"k is negative":         {"[retrieval.fusion]\nk = -1\n", "retrieval.fusion.k"},
		"weights not a table":   {"[retrieval.fusion]\nweights = 1.0\n", "retrieval.fusion.weights"},
		"weight not a number":   {"[retrieval.fusion]\nweights = { fts5 = \"heavy\" }\n", "retrieval.fusion.weights.fts5"},
		"weight negative":       {"[retrieval.fusion]\nweights = { fts5 = -0.5 }\n", "retrieval.fusion.weights.fts5"},
		"weight not finite":     {"[retrieval.fusion]\nweights = { fts5 = nan }\n", "retrieval.fusion.weights.fts5"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadRetrieval(t, tc.body, nil)
			if err == nil {
				t.Fatal("malformed retrieval config loaded successfully")
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

// TestRetrievalWeightInfinityRefused covers the infinity half of the
// finiteness check, which the TOML `inf` literal reaches.
func TestRetrievalWeightInfinityRefused(t *testing.T) {
	_, err := loadRetrieval(t, "[retrieval.fusion]\nweights = { fts5 = inf }\n", nil)
	if err == nil {
		t.Fatal("an infinite weight loaded successfully")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) || cfgErr.Field != "retrieval.fusion.weights.fts5" {
		t.Fatalf("err = %v, want a *ConfigError naming the weight", err)
	}
}

// TestRetrievalEmptyWeightsTableIsNil: `weights = {}` configures no
// weights, which is the same as not writing the key. Reporting an empty
// non-nil map would make "unconfigured" and "configured to nothing" look
// different to the consumer when they are not.
func TestRetrievalEmptyWeightsTableIsNil(t *testing.T) {
	cfg, err := loadRetrieval(t, "[retrieval.fusion]\nweights = {}\n", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FusionWeights() != nil {
		t.Errorf("FusionWeights() = %v, want nil", cfg.FusionWeights())
	}
}

// TestRetrievalFusionKEnvOverride: the generic
// CASCADE_<SECTION>__<KEY> machinery reaches fusion.k and yields a real
// integer the validator then checks.
func TestRetrievalFusionKEnvOverride(t *testing.T) {
	cfg, err := loadRetrieval(t, "[retrieval.fusion]\nk = 60\n",
		map[string]string{"CASCADE_RETRIEVAL__FUSION__K": "17"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.FusionK(); got != 17 {
		t.Errorf("FusionK() = %d, want the env override 17", got)
	}
}

// TestRetrievalFusionKEnvOverrideFailsClosed: an env override is held to
// the same rules as the file. An operator exporting a bad value must hear
// about it rather than silently getting the default.
func TestRetrievalFusionKEnvOverrideFailsClosed(t *testing.T) {
	_, err := loadRetrieval(t, "[retrieval.fusion]\nk = 60\n",
		map[string]string{"CASCADE_RETRIEVAL__FUSION__K": "0"})
	if err == nil {
		t.Fatal("an env override of k = 0 loaded successfully")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) || cfgErr.Field != "retrieval.fusion.k" {
		t.Fatalf("err = %v, want a *ConfigError naming retrieval.fusion.k", err)
	}
}

// TestToFloatRejectsNonNumeric covers the widening helper's refusal path
// directly, including the NaN it must not smuggle through as "a number".
func TestToFloatRejectsNonNumeric(t *testing.T) {
	if _, ok := toFloat("1.0"); ok {
		t.Error("a string was accepted as a number")
	}
	v, ok := toFloat(math.NaN())
	if !ok || !math.IsNaN(v) {
		t.Error("toFloat did not pass a float64 NaN through for the finiteness check to reject")
	}
}
