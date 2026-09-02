// Purpose: loader-path tests (strict-mode unknown-key rejection, malformed
// TOML) plus the ValidationError/ErrCode formatting and mapping tests.
// Split out of manifest_test.go per R-14.117 (Art.10.3's 300-line file
// cap) — a behaviour-preserving relocation; no assertion, name, or
// signature changed.
// Constraints: no network calls (Art.7.2); no writes at all.
package plugin_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// TestParseManifest_StrictUnknownKey covers the strict-mode unknown-key
// rejection acceptance criterion: an unrecognized top-level key must be
// rejected with ErrCodeParse, not silently ignored.
func TestParseManifest_StrictUnknownKey(t *testing.T) {
	doc := baseManifestTOML + "\nbogus_top_level_key = \"surprise\"\n"

	m, err := plugin.ParseManifest(strings.NewReader(doc))
	if err == nil {
		t.Fatal("ParseManifest(unknown top-level key) = _, nil, want ErrCodeParse")
	}
	if !reflect.DeepEqual(m, plugin.Manifest{}) {
		t.Errorf("ParseManifest(unknown key): got non-zero Manifest %+v alongside error", m)
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindInvalidInput {
		t.Errorf("ParseManifest(unknown key): cascade.Kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
	if !strings.Contains(err.Error(), "bogus_top_level_key") {
		t.Errorf("ParseManifest(unknown key): error %q does not name the offending key", err.Error())
	}
}

// TestParseManifest_MalformedTOML covers a syntactically broken document
// (not just an unknown key), asserting it also fail-closes.
func TestParseManifest_MalformedTOML(t *testing.T) {
	m, err := plugin.ParseManifest(strings.NewReader("id = \"unterminated"))
	if err == nil {
		t.Fatal("ParseManifest(malformed TOML) = _, nil, want a decode error")
	}
	if !reflect.DeepEqual(m, plugin.Manifest{}) {
		t.Errorf("ParseManifest(malformed TOML): got non-zero Manifest %+v alongside error", m)
	}
}

// TestValidationError_Error covers ValidationError's Error() string
// formatting directly (exercised indirectly via cascade.Wrap in the other
// tests, but never asserted on its own literal form).
func TestValidationError_Error(t *testing.T) {
	ve := plugin.ValidationError{Field: "id", Kind: plugin.ErrCodeRequiredField, Message: "id must not be empty"}
	want := "id: required-field: id must not be empty"
	if got := ve.Error(); got != want {
		t.Errorf("ValidationError.Error() = %q, want %q", got, want)
	}
}

// TestErrCode_KindMapping covers ErrCode.Kind for every named constant plus
// the unrecognized-value fallback to cascade.KindInternal.
func TestErrCode_KindMapping(t *testing.T) {
	tests := map[plugin.ErrCode]cascade.Kind{
		plugin.ErrCodeParse:                 cascade.KindInvalidInput,
		plugin.ErrCodeSchemaVersion:         cascade.KindUnsupported,
		plugin.ErrCodeUnknownRuntimeMode:    cascade.KindInvalidInput,
		plugin.ErrCodeRequiredField:         cascade.KindInvalidInput,
		plugin.ErrCodeMalformedVersion:      cascade.KindInvalidInput,
		plugin.ErrCodeCommandNameCollision:  cascade.KindConflict,
		plugin.ErrCodeInvalidCapabilityRef:  cascade.KindInvalidInput,
		plugin.ErrCode("unrecognized-code"): cascade.KindInternal,
	}
	for code, want := range tests {
		if got := code.Kind(); got != want {
			t.Errorf("ErrCode(%q).Kind() = %v, want %v", code, got, want)
		}
	}
}
