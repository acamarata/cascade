package secrets

// Purpose: the tag grammar's tests. Round-trip for every accepted form,
//
//	a refusal for every malformed one, and the fixed detection-class to
//	tag-type table asserted against the registry that emits the classes
//	rather than against a second copy of the same table.
//
// Constraints: the malformed corpus is the same set of shapes the fuzz
//
//	seed corpus carries, so a change that makes one of them parse fails
//	here first, in a named test, rather than as a fuzz crash months later.

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestTagRoundTrip renders and re-parses every accepted tag form.
func TestTagRoundTrip(t *testing.T) {
	tags := []Tag{
		{Type: TagPassword, Name: "WIFI_PASSWORD"},
		{Type: TagAPIKey, Name: "OPENAI_API_KEY"},
		{Type: TagToken, Name: "BEARER_TOKEN"},
		{Type: TagConnStr, Name: "DATABASE_URL"},
		{Type: TagPII, Name: "TAXPAYER_ID", PIIKind: "ssn"},
		{Type: TagPII, Name: "BIRTH_DATE", PIIKind: "dob"},
		{Type: TagPII, Name: "BANK_ACCOUNT", PIIKind: "account"},
		{Type: TagAPIKey, Name: "K"},
		{Type: TagToken, Name: "T0_9_Z"},
	}
	for _, want := range tags {
		rendered := want.String()
		if rendered == "" {
			t.Fatalf("%+v rendered as the empty string", want)
		}
		got, err := ParseTag([]byte(rendered))
		if err != nil {
			t.Fatalf("ParseTag(%q): %v", rendered, err)
		}
		if got != want {
			t.Fatalf("ParseTag(%q) = %+v, want %+v", rendered, got, want)
		}
		if got.String() != rendered {
			t.Fatalf("re-rendering %q produced %q", rendered, got.String())
		}
	}
}

// malformedTags is the refusal corpus. Every entry here is also a seed in
// internal/testdata/fuzz/FuzzTagParser.
var malformedTags = map[string]string{
	"truncated":            "<apikey>OPENAI_API_KEY",
	"unterminated-open":    "<apikey",
	"mismatched-close":     "<password>WIFI_PASSWORD</token>",
	"missing-close":        "<password>WIFI_PASSWORD",
	"unknown-type":         "<secret>WIFI_PASSWORD</secret>",
	"illegal-name-space":   "<apikey>OPENAI API KEY</apikey>",
	"illegal-name-lower":   "<apikey>openai_api_key</apikey>",
	"illegal-name-hyphen":  "<apikey>OPENAI-API-KEY</apikey>",
	"illegal-name-leading": "<apikey>1_KEY</apikey>",
	"empty-name":           "<connstr></connstr>",
	"pii-missing-kind":     "<pii>TAXPAYER_ID</pii>",
	"pii-unknown-kind":     "<pii kind=\"passport\">TRAVEL_DOC</pii>",
	"pii-unquoted-kind":    "<pii kind=ssn>TAXPAYER_ID</pii>",
	"kind-on-typed-tag":    "<password kind=\"ssn\">WIFI_PASSWORD</password>",
	"nested":               "<password><token>INNER_TOKEN</token></password>",
	"non-ascii-name":       "<token>トークン</token>",
	"empty":                "",
	"prose":                "the password is written down",
	"leading-text":         "note <apikey>OPENAI_API_KEY</apikey>",
	"trailing-text":        "<apikey>OPENAI_API_KEY</apikey> note",
	"two-tags":             "<apikey>A_KEY</apikey><apikey>B_KEY</apikey>",
	"invalid-utf8":         "<apikey>OPENAI_\xff_KEY</apikey>",
}

// TestParseTagRefusesMalformed asserts every malformed shape is refused
// with the taxonomy's invalid-input kind, never parsed leniently.
func TestParseTagRefusesMalformed(t *testing.T) {
	for name, raw := range malformedTags {
		tag, err := ParseTag([]byte(raw))
		if err == nil {
			t.Fatalf("%s: ParseTag(%q) returned %+v, want an error", name, raw, tag)
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: ParseTag(%q) error kind = %v, want invalid input", name, raw, err)
		}
		if tag != (Tag{}) {
			t.Fatalf("%s: ParseTag(%q) returned a tag alongside its error: %+v", name, raw, tag)
		}
	}
}

// TestTagValidateRejectsInvalidValues covers the Tag values a caller can
// build directly, which never pass through the parser.
func TestTagValidateRejectsInvalidValues(t *testing.T) {
	cases := map[string]Tag{
		"zero value":       {},
		"unknown type":     {Type: TagType("secret"), Name: "A_KEY"},
		"empty name":       {Type: TagAPIKey},
		"pii without kind": {Type: TagPII, Name: "TAXPAYER_ID"},
		"pii bad kind":     {Type: TagPII, Name: "TAXPAYER_ID", PIIKind: "passport"},
		"kind on non-pii":  {Type: TagAPIKey, Name: "A_KEY", PIIKind: "ssn"},
		"over-long name":   {Type: TagAPIKey, Name: strings.Repeat("A", maxSecretNameLen+1)},
	}
	for name, tag := range cases {
		if err := tag.Validate(); err == nil {
			t.Fatalf("%s: Validate accepted %+v", name, tag)
		}
		if rendered := tag.String(); rendered != "" {
			t.Fatalf("%s: an invalid tag rendered as %q", name, rendered)
		}
	}
}

// TestTagForEveryDetectionClass asserts the fixed table covers every class
// the default registry emits, and that it maps each one to the type the
// ruling names. The expectations are written out here rather than read
// back from tagTypeForClass, so this is a check against the spec and not
// a second copy of the table agreeing with itself.
func TestTagForEveryDetectionClass(t *testing.T) {
	want := map[Class]TagType{
		ClassAPIKey:     TagAPIKey,
		ClassBase64JSON: TagAPIKey,
		ClassJWT:        TagToken,
		ClassBearer:     TagToken,
		ClassPEM:        TagPassword,
		ClassConnString: TagConnStr,
	}
	seen := map[Class]bool{}
	for _, pattern := range DefaultRegistry().Patterns() {
		seen[pattern.Class] = true
		expected, mapped := want[pattern.Class]
		if !mapped {
			t.Fatalf("the registry emits class %q, which the R-21.240 table does not name", pattern.Class)
		}
		got, err := TagFor(pattern.Class)
		if err != nil {
			t.Fatalf("TagFor(%q): %v", pattern.Class, err)
		}
		if got != expected {
			t.Fatalf("TagFor(%q) = %q, want %q", pattern.Class, got, expected)
		}
	}
	for class := range want {
		if !seen[class] {
			t.Fatalf("the table names class %q, which the registry never emits", class)
		}
	}
}

// TestTagForUnmappedClass covers the two classes with no row: the
// detector's shape-only signal, and anything unknown. Both must return an
// error and an empty type, never a zero TagType passed off as a mapping.
func TestTagForUnmappedClass(t *testing.T) {
	for _, class := range []Class{ClassHighEntropy, Class(""), Class("invented-class")} {
		got, err := TagFor(class)
		if err == nil {
			t.Fatalf("TagFor(%q) returned %q with no error", class, got)
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("TagFor(%q) error kind = %v, want invalid input", class, err)
		}
		if got != "" {
			t.Fatalf("TagFor(%q) returned type %q alongside its error", class, got)
		}
	}
}

// TestPIIIsDetectorUnproduced pins the R-21.240 position: the grammar
// round-trips hand-authored pii tags, and no detection class maps to one,
// so nothing in this package can emit one.
func TestPIIIsDetectorUnproduced(t *testing.T) {
	for _, kind := range piiKinds {
		raw := "<pii kind=\"" + kind + "\">TAXPAYER_ID</pii>"
		tag, err := ParseTag([]byte(raw))
		if err != nil {
			t.Fatalf("ParseTag(%q): %v", raw, err)
		}
		if tag.String() != raw {
			t.Fatalf("round-tripping %q produced %q", raw, tag.String())
		}
	}
	for _, row := range tagTypeForClass {
		if row.Type == TagPII {
			t.Fatalf("class %q maps to the pii tag type, which no detector produces", row.Class)
		}
	}
}
