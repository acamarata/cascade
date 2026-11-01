package runtime

import (
	"errors"
	"testing"
	"time"
)

// Purpose: config_retrieval_embedding.go's tests: the defaults, the closed
//   key set, the lane rule and the timeout bounds.
// Constraints: every case writes its config.toml under t.TempDir() and
//   injects Getenv/Environ (loadRetrieval).

// wantConfigField asserts err is a *ConfigError naming field.
func wantConfigField(t *testing.T, err error, field string) {
	t.Helper()
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("err = %v, want a *ConfigError for %s", err, field)
	}
	if cfgErr.Field != field {
		t.Fatalf("Field = %q, want %q (err %v)", cfgErr.Field, field, err)
	}
}

func TestRetrievalEmbeddingDefaults(t *testing.T) {
	want := EmbeddingSettings{LocalModel: "bge-m3", LocalDimensions: 1024, Timeout: 30 * time.Second}
	for name, body := range map[string]string{
		"no file content":   "",
		"no embedding":      "[retrieval]\nsources = []\n",
		"empty embedding":   "[retrieval.embedding]\n",
		"socket only (key)": "[retrieval.embedding]\nlocal_socket = \"\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadRetrieval(t, body, nil)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.RetrievalEmbedding(); got != want {
				t.Fatalf("RetrievalEmbedding = %+v, want %+v", got, want)
			}
		})
	}
	if got := (*Config)(nil).RetrievalEmbedding(); got != want {
		t.Fatalf("nil config = %+v, want %+v", got, want)
	}
	body := "[retrieval.embedding]\nlocal_model = \"m\"\nlocal_dimensions = 8\nlane = \"emb\"\nmodel = \"x\"\ndimensions = 16\ntimeout = \"5s\"\n"
	cfg, err := loadRetrieval(t, body, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	full := EmbeddingSettings{LocalModel: "m", LocalDimensions: 8, Lane: "emb", Model: "x", Dimensions: 16, Timeout: 5 * time.Second}
	if got := cfg.RetrievalEmbedding(); got != full {
		t.Fatalf("RetrievalEmbedding = %+v, want %+v", got, full)
	}
}

func TestRetrievalEmbeddingRejectsUnknownKey(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"misspelled key":      {"[retrieval.embedding]\nlocal_sockets = \"/x\"\n", "retrieval.embedding.local_sockets"},
		"foreign key":         {"[retrieval.embedding]\nprovider = \"openai\"\n", "retrieval.embedding.provider"},
		"embedding not table": {"[retrieval]\nembedding = \"bge\"\n", "retrieval.embedding"},
		"model wrong type":    {"[retrieval.embedding]\nlocal_model = 3\n", "retrieval.embedding.local_model"},
		"dimensions negative": {"[retrieval.embedding]\nlocal_dimensions = -1\n", "retrieval.embedding.local_dimensions"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadRetrieval(t, tc.body, nil)
			wantConfigField(t, err, tc.field)
		})
	}
}

func TestRetrievalEmbeddingLaneRequiresModelAndDimensions(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"no model":      {"[retrieval.embedding]\nlane = \"emb\"\ndimensions = 8\n", "retrieval.embedding.model"},
		"no dimensions": {"[retrieval.embedding]\nlane = \"emb\"\nmodel = \"x\"\n", "retrieval.embedding.dimensions"},
		"neither":       {"[retrieval.embedding]\nlane = \"emb\"\n", "retrieval.embedding.model"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadRetrieval(t, tc.body, nil)
			wantConfigField(t, err, tc.field)
		})
	}
	if _, err := loadRetrieval(t, "[retrieval.embedding]\nlane = \"emb\"\nmodel = \"x\"\ndimensions = 8\n", nil); err != nil {
		t.Fatalf("a complete lane was refused: %v", err)
	}
}

func TestRetrievalEmbeddingTimeoutBounds(t *testing.T) {
	for _, bad := range []string{`"0s"`, `"-1s"`, `"61s"`, `"2m"`, `"soon"`, `30`} {
		t.Run(bad, func(t *testing.T) {
			_, err := loadRetrieval(t, "[retrieval.embedding]\ntimeout = "+bad+"\n", nil)
			wantConfigField(t, err, "retrieval.embedding.timeout")
		})
	}
	for raw, want := range map[string]time.Duration{`"1ns"`: time.Nanosecond, `"60s"`: 60 * time.Second, `"1m"`: time.Minute} {
		cfg, err := loadRetrieval(t, "[retrieval.embedding]\ntimeout = "+raw+"\n", nil)
		if err != nil {
			t.Fatalf("timeout %s refused: %v", raw, err)
		}
		if got := cfg.RetrievalEmbedding().Timeout; got != want {
			t.Fatalf("timeout %s = %s, want %s", raw, got, want)
		}
	}
}
