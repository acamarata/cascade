// Purpose: the R1-R6 rejection-rule test table plus the R5 core-noun
// collision and multi-error-ordering tests. Split out of manifest_test.go
// per R-14.117 (Art.10.3's 300-line file cap) — a behaviour-preserving
// relocation; no assertion, name, or signature changed.
// Constraints: no network calls (Art.7.2); no writes at all.
package plugin_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// rejectionRuleCase is one row of the R1-R6 rejection-rule test table.
type rejectionRuleCase struct {
	name    string
	doc     string
	field   string
	errCode plugin.ErrCode
}

// rejectionRuleCasesSchemaRuntimeRequired covers R1 (schema), R2 (runtime),
// and R3 (required id/name). Split out of rejectionRuleCases to keep both
// under Art.10.3's 50-line function cap.
func rejectionRuleCasesSchemaRuntimeRequired() []rejectionRuleCase {
	return []rejectionRuleCase{
		{
			name:    "R1 schema version mismatch",
			doc:     strings.Replace(baseManifestTOML, `schema = "cascade.plugin/v2"`, `schema = "cascade.plugin/v1"`, 1),
			field:   "schema",
			errCode: plugin.ErrCodeSchemaVersion,
		},
		{
			name:    "R2 unknown runtime mode",
			doc:     strings.Replace(baseManifestTOML, `runtime = "builtin"`, `runtime = "sidecar"`, 1),
			field:   "runtime",
			errCode: plugin.ErrCodeUnknownRuntimeMode,
		},
		{
			name:    "R3 empty id",
			doc:     strings.Replace(baseManifestTOML, `id = "test-plugin"`, `id = ""`, 1),
			field:   "id",
			errCode: plugin.ErrCodeRequiredField,
		},
		{
			name:    "R3 empty name",
			doc:     strings.Replace(baseManifestTOML, `name = "Test Plugin"`, `name = ""`, 1),
			field:   "name",
			errCode: plugin.ErrCodeRequiredField,
		},
	}
}

// rejectionRuleCasesVersionsAndRequires covers R4 (malformed version /
// host_version) and R6 (empty/duplicate requires entries).
func rejectionRuleCasesVersionsAndRequires() []rejectionRuleCase {
	return []rejectionRuleCase{
		{
			name:    "R4 malformed version",
			doc:     strings.Replace(baseManifestTOML, `version = "1.0.0"`, `version = "not-a-semver"`, 1),
			field:   "version",
			errCode: plugin.ErrCodeMalformedVersion,
		},
		{
			name:    "R4 malformed host_version",
			doc:     strings.Replace(baseManifestTOML, `host_version = ">=2.0.0"`, `host_version = "???"`, 1),
			field:   "host_version",
			errCode: plugin.ErrCodeMalformedVersion,
		},
		{
			name:    "R4 empty host_version",
			doc:     strings.Replace(baseManifestTOML, `host_version = ">=2.0.0"`, `host_version = "   "`, 1),
			field:   "host_version",
			errCode: plugin.ErrCodeMalformedVersion,
		},
		{
			name:    "R6 empty requires entry",
			doc:     strings.Replace(baseManifestTOML, `requires = ["storage.domain"]`, `requires = [""]`, 1),
			field:   "requires[0]",
			errCode: plugin.ErrCodeInvalidCapabilityRef,
		},
		{
			name:    "R6 duplicate requires entry",
			doc:     strings.Replace(baseManifestTOML, `requires = ["storage.domain"]`, `requires = ["storage.domain", "storage.domain"]`, 1),
			field:   "requires[1]",
			errCode: plugin.ErrCodeInvalidCapabilityRef,
		},
	}
}

// rejectionRuleCases builds the full R1-R6 test table: each case overrides
// exactly the field(s) needed to trigger one rule against baseManifestTOML,
// isolating it from the other five.
func rejectionRuleCases() []rejectionRuleCase {
	cases := rejectionRuleCasesSchemaRuntimeRequired()
	return append(cases, rejectionRuleCasesVersionsAndRequires()...)
}

// TestValidate_RejectionRules covers all six rejection rules R1-R6, each
// asserting the ValidationError's expected ErrCode, and confirms the
// fail-closed invariant: ParseManifest never returns a non-zero Manifest
// alongside a non-nil error.
func TestValidate_RejectionRules(t *testing.T) {
	for _, tt := range rejectionRuleCases() {
		t.Run(tt.name, func(t *testing.T) {
			m, err := plugin.ParseManifest(strings.NewReader(tt.doc))
			if err == nil {
				t.Fatalf("ParseManifest(%s) = _, nil, want a validation error", tt.name)
			}
			// Fail-closed invariant: never a populated Manifest alongside
			// a non-nil error.
			if !reflect.DeepEqual(m, plugin.Manifest{}) {
				t.Errorf("ParseManifest(%s): got non-zero Manifest %+v alongside error %v", tt.name, m, err)
			}
			kind, ok := cascade.KindOf(err)
			if !ok {
				t.Fatalf("ParseManifest(%s): error %v carries no cascade.Kind", tt.name, err)
			}
			if want := tt.errCode.Kind(); kind != want {
				t.Errorf("ParseManifest(%s): cascade.Kind = %v, want %v", tt.name, kind, want)
			}

			var ve plugin.ValidationError
			if errFound := errAs(err, &ve); !errFound {
				t.Fatalf("ParseManifest(%s): error chain has no plugin.ValidationError", tt.name)
			}
			if ve.Kind != tt.errCode {
				t.Errorf("ParseManifest(%s): ValidationError.Kind = %v, want %v", tt.name, ve.Kind, tt.errCode)
			}
			if ve.Field != tt.field {
				t.Errorf("ParseManifest(%s): ValidationError.Field = %q, want %q", tt.name, ve.Field, tt.field)
			}
		})
	}
}

// TestValidate_R5CoreNounCollision covers rule R5 for at least five
// reserved core-noun/utility-verb names, per the ticket's acceptance
// criteria.
func TestValidate_R5CoreNounCollision(t *testing.T) {
	reserved := []string{"config", "plugin", "vault", "daemon", "init"}
	for _, name := range reserved {
		t.Run(name, func(t *testing.T) {
			doc := baseManifestTOML + "\n[[provides.commands]]\nname = \"" + name + "\"\ndescription = \"x\"\n"
			m, err := plugin.ParseManifest(strings.NewReader(doc))
			if err == nil {
				t.Fatalf("ParseManifest(commands.name=%q) = _, nil, want ErrCodeCommandNameCollision", name)
			}
			if !reflect.DeepEqual(m, plugin.Manifest{}) {
				t.Errorf("ParseManifest(commands.name=%q): got non-zero Manifest alongside error", name)
			}
			var ve plugin.ValidationError
			if !errAs(err, &ve) {
				t.Fatalf("ParseManifest(commands.name=%q): error chain has no plugin.ValidationError", name)
			}
			if ve.Kind != plugin.ErrCodeCommandNameCollision {
				t.Errorf("ParseManifest(commands.name=%q): ValidationError.Kind = %v, want ErrCodeCommandNameCollision", name, ve.Kind)
			}
		})
	}
}

// TestValidate_MultipleErrorsOrderedByFieldOccurrence confirms Validate
// does not short-circuit: a manifest with several simultaneous problems
// reports all of them, in the Manifest struct's field-declaration order.
func TestValidate_MultipleErrorsOrderedByFieldOccurrence(t *testing.T) {
	m := plugin.Manifest{
		ID:          "",
		Name:        "",
		Schema:      "wrong-schema",
		Version:     "not-semver",
		HostVersion: "also-not-a-range!!",
		Runtime:     "bogus-runtime",
		Requires:    []string{""},
	}
	errs := plugin.Validate(m)
	if len(errs) < 6 {
		t.Fatalf("Validate(multi-bad manifest) = %d errors, want at least 6: %+v", len(errs), errs)
	}
	wantOrder := []string{"id", "name", "schema", "version", "host_version", "runtime"}
	for i, field := range wantOrder {
		if errs[i].Field != field {
			t.Errorf("errs[%d].Field = %q, want %q (field-occurrence order)", i, errs[i].Field, field)
		}
	}
}
