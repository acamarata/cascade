// Package topics (taxonomy_test.go): Purpose: TaxonomyConfig's injected
// map, its configured fallback, unknown-label resolution (including the
// empty string), copy-on-construct isolation, and the TopicType key-space
// boundary.
//
// The mapped-label case is driven from a provenance-stamped fixture
// (testdata/taxonomy/plugin-default-labels.json) rather than a map literal
// written a few lines above the assertion: a test that builds {"code":
// "code-topic"} and then asserts Resolve("code") == "code-topic" is a
// restatement of the map lookup and cannot fail for any reason worth
// knowing about. The fixture records the label set R-14.63 says the
// cascade-pa plugin injects, together with the expectations that set must
// produce - including the case-sensitivity, no-trimming and empty-label
// cases nothing else in this file would have pinned.
package topics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// taxonomyFixture is testdata/taxonomy/plugin-default-labels.json's shape.
type taxonomyFixture struct {
	Provenance   map[string]string    `json:"provenance"`
	Fallback     TopicType            `json:"fallback"`
	Labels       map[string]TopicType `json:"labels"`
	Expectations []struct {
		Label string    `json:"label"`
		Topic TopicType `json:"topic"`
		Why   string    `json:"why"`
	} `json:"expectations"`
}

func loadTaxonomyFixture(t *testing.T) taxonomyFixture {
	t.Helper()
	path := filepath.Join("testdata", "taxonomy", "plugin-default-labels.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var fx taxonomyFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	if len(fx.Labels) == 0 || len(fx.Expectations) == 0 || fx.Fallback == "" {
		t.Fatalf("%s must carry labels, a fallback and expectations; got %+v", path, fx)
	}
	if fx.Provenance["captured_on"] == "" || fx.Provenance["source"] == "" {
		t.Fatalf("%s must state its provenance (source and capture date) per Art.2.2; got %+v", path, fx.Provenance)
	}
	return fx
}

// TestTaxonomyConfigResolvesTheInjectedLabelSet drives every expectation
// the fixture records through one TaxonomyConfig built from the fixture's
// own label map: mapped labels reach their canonical topic, and every
// variant the plugin did not declare reaches the configured fallback.
func TestTaxonomyConfigResolvesTheInjectedLabelSet(t *testing.T) {
	fx := loadTaxonomyFixture(t)
	cfg := NewTaxonomyConfig(fx.Labels, fx.Fallback)
	for _, want := range fx.Expectations {
		if got := cfg.Resolve(want.Label); got != want.Topic {
			t.Errorf("Resolve(%q) = %q, want %q (%s)", want.Label, got, want.Topic, want.Why)
		}
	}
}

// TestTaxonomyConfigResolveUnknownLabelReturnsFallback keeps the fallback
// path pinned independently of the fixture, so the rule survives an edit to
// the fixture's expectations.
func TestTaxonomyConfigResolveUnknownLabelReturnsFallback(t *testing.T) {
	cfg := NewTaxonomyConfig(map[string]TopicType{"code": TopicType("code-topic")}, TopicType("fallback"))
	for _, label := range []string{"unmapped", "CODE", "code ", ""} {
		if got := cfg.Resolve(label); got != TopicType("fallback") {
			t.Fatalf("Resolve(%q) = %q, want the configured fallback %q", label, got, "fallback")
		}
	}
}

// TestTaxonomyConfigResolveNeverPanics is the mutation-facing proof for
// R-14.63's "resolves to the configured fallback without panicking for any
// unknown label string": a nil map, a zero-value config, and a fallback
// that is itself the empty string all still return cleanly rather than
// panicking on a nil-map read.
func TestTaxonomyConfigResolveNeverPanics(t *testing.T) {
	var zero TaxonomyConfig
	if got := zero.Resolve("anything"); got != TopicType("") {
		t.Fatalf("zero-value TaxonomyConfig.Resolve(anything) = %q, want the empty TopicType", got)
	}
	cfg := NewTaxonomyConfig(nil, TopicType(""))
	if got := cfg.Resolve(""); got != TopicType("") {
		t.Fatalf("Resolve(\"\") with nil labels = %q, want the empty TopicType", got)
	}
}

// TestTaxonomyConfigCopiesLabelsOnConstruct proves NewTaxonomyConfig does
// not alias the caller's map: mutating the original after construction
// must never change what an already-built TaxonomyConfig resolves.
func TestTaxonomyConfigCopiesLabelsOnConstruct(t *testing.T) {
	labels := map[string]TopicType{"code": TopicType("code-topic")}
	cfg := NewTaxonomyConfig(labels, TopicType("fallback"))
	labels["code"] = TopicType("mutated")
	labels["new"] = TopicType("also-mutated")
	if got := cfg.Resolve("code"); got != TopicType("code-topic") {
		t.Fatalf("Resolve(code) after mutating the source map = %q, want the original code-topic (map was aliased, not copied)", got)
	}
	if got := cfg.Resolve("new"); got != TopicType("fallback") {
		t.Fatalf("Resolve(new) = %q, want fallback (map was aliased, not copied)", got)
	}
}

// TestTaxonomyConfigCoreShipsNoBuiltInLabels is R-14.63's own assertion,
// checked against the very label set the fixture says the plugin injects:
// a TaxonomyConfig built with no labels must know none of them.
func TestTaxonomyConfigCoreShipsNoBuiltInLabels(t *testing.T) {
	fx := loadTaxonomyFixture(t)
	cfg := NewTaxonomyConfig(nil, TopicType("unclassified"))
	for label := range fx.Labels {
		if got := cfg.Resolve(label); got != TopicType("unclassified") {
			t.Fatalf("Resolve(%q) = %q on a TaxonomyConfig built with no labels, want the fallback: "+
				"core must not ship a built-in default label set (R-14.63)", label, got)
		}
	}
}

// TestValidateTopicTypeBounds pins the key-space boundary every resolved
// TopicType crosses before it becomes a storage key or a thread id.
func TestValidateTopicTypeBounds(t *testing.T) {
	for _, good := range []TopicType{"code", "general", "code-topic", "a.b_c:d", "T9"} {
		if err := validateTopicType(good); err != nil {
			t.Fatalf("validateTopicType(%q) = %v, want nil", good, err)
		}
	}
	long := TopicType(make([]byte, 0))
	for len(long) <= topicTypeMaxLen {
		long += "x"
	}
	for _, bad := range []TopicType{"", "has space", "slash/es", "new\nline", "quote\"d", long} {
		if err := validateTopicType(bad); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("validateTopicType(%q) = %v, want KindInvalidInput", bad, err)
		}
	}
	if err := validateTopicType(long); err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("validateTopicType over the %d-byte bound = %v, want KindInvalidInput", topicTypeMaxLen, err)
	}
}
