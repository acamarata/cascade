// Purpose: the typed-tag grammar. A tag is what a credential span becomes
//
//	once the rewriter has taken the value out of a turn: a type, a vault
//	NAME, and nothing else. The grammar is the whole surface between the
//	rewrite direction (this ticket) and the rehydrate direction, so it is
//	parsed strictly and written in exactly one way.
//
// Inputs: tag bytes (ParseTag), or a Class to be mapped (TagFor).
// Outputs: a Tag, its rendering, or a KindInvalidInput error naming what
//
//	was wrong with the input. Never the credential a tag stands for: a
//	Tag holds a NAME, and a NAME is a vault reference, not a value.
//
// Constraints: fail closed and total. Every malformed input is refused
//
//	rather than best-effort repaired, because a tag that parses into
//	something other than what it says would rehydrate the wrong secret,
//	and a tag that is silently treated as prose would put a value back
//	into a turn the user believed was scrubbed. No I/O, no clock, no
//	randomness; identical bytes in, identical Tag out.
//
// SPORT: SECRETS_TAGS: ADD (internal/secrets.TagType, Tag).
//
//	SECRETS_TAG_MAP: ADD (internal/secrets.TagFor).

package secrets

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TagType is the kind of credential a tag stands in for. The four
// detector-produced types name a shape of credential; TagPII is the
// hand-authored one - see TagFor for why no detector emits it.
type TagType string

// The tag types the grammar accepts. These strings ARE the wire form:
// the tag `<apikey>NAME</apikey>` is spelled with the constant's value,
// so renaming one is a format change, not a refactor.
const (
	// TagPassword stands for a password or a private key.
	TagPassword TagType = "password"
	// TagAPIKey stands for an API key or an encoded credential blob.
	TagAPIKey TagType = "apikey"
	// TagToken stands for a bearer token or a JWT.
	TagToken TagType = "token"
	// TagConnStr stands for a connection string with inline credentials.
	TagConnStr TagType = "connstr"
	// TagPII stands for a personal identifier. It carries a kind
	// attribute and is never produced by a detector in this phase.
	TagPII TagType = "pii"
)

// tagTypes is the fixed, ordered set of accepted types. Ordered rather
// than a map because scanTags walks it to recognise a tag in text, and
// Art.7 determinism forbids that walk from depending on map order.
var tagTypes = []TagType{TagPassword, TagAPIKey, TagToken, TagConnStr, TagPII}

// piiKinds is the fixed set of `kind` attribute values a pii tag accepts.
// An unknown kind is refused: a tag whose kind the grammar does not know
// cannot be rehydrated correctly, and guessing is how the wrong field
// gets injected into a request.
var piiKinds = []string{"ssn", "dob", "account"}

// Tag is one typed placeholder. Name is a vault reference in UPPER_SNAKE
// ([A-Z][A-Z0-9_]*), never a value; PIIKind is set only when Type is
// TagPII and is empty otherwise.
type Tag struct {
	// Type is the credential kind this tag stands in for.
	Type TagType `json:"type"`
	// Name is the UPPER_SNAKE vault reference the value is stored under.
	Name string `json:"name"`
	// PIIKind is the pii tag's kind attribute; empty for other types.
	PIIKind string `json:"pii_kind,omitempty"`
}

// String renders the tag in its single canonical form. A Tag that does
// not satisfy the grammar renders as the empty string rather than as
// something that would parse back as a different tag; callers that build
// a Tag from untrusted parts check Validate first, and Rewriter does.
func (t Tag) String() string {
	if t.Validate() != nil {
		return ""
	}
	if t.Type == TagPII {
		return `<pii kind="` + t.PIIKind + `">` + t.Name + `</pii>`
	}
	return "<" + string(t.Type) + ">" + t.Name + "</" + string(t.Type) + ">"
}

// Validate reports whether the tag satisfies the grammar: a known type, a
// legal NAME, and a kind attribute present exactly when the type is pii.
func (t Tag) Validate() error {
	if !knownTagType(t.Type) {
		return cascade.Newf(cascade.KindInvalidInput, "secrets: unknown tag type %q", string(t.Type))
	}
	if err := validateTagName(t.Name); err != nil {
		return err
	}
	if t.Type != TagPII {
		if t.PIIKind != "" {
			return cascade.Newf(cascade.KindInvalidInput,
				"secrets: a <%s> tag must not carry a kind attribute", string(t.Type))
		}
		return nil
	}
	if !knownPIIKind(t.PIIKind) {
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: a <pii> tag needs kind=\"ssn|dob|account\", got %q", t.PIIKind)
	}
	return nil
}

// ParseTag decodes exactly one tag. The whole input must be the tag: a
// tag with anything before or after it is refused, because this parser is
// also what decides whether a run of text in a turn IS a tag, and a
// lenient answer there would let ordinary prose shield a credential.
//
// The contract names this entry point Tag.Parse; it is a package
// function because it constructs a Tag rather than reading one, and a
// value-receiver method that ignores its receiver reads as an accident.
func ParseTag(raw []byte) (Tag, error) {
	text := string(raw)
	if len(text) == 0 || text[0] != '<' {
		return Tag{}, cascade.New(cascade.KindInvalidInput, "secrets: a tag must start with '<'")
	}
	end := strings.IndexByte(text, '>')
	if end < 0 {
		return Tag{}, cascade.New(cascade.KindInvalidInput, "secrets: the opening tag is unterminated")
	}
	tag, err := parseTagHead(text[1:end])
	if err != nil {
		return Tag{}, err
	}
	closing := "</" + string(tag.Type) + ">"
	body := text[end+1:]
	if !strings.HasSuffix(body, closing) {
		return Tag{}, cascade.Newf(cascade.KindInvalidInput,
			"secrets: a <%s> tag must end with %s", string(tag.Type), closing)
	}
	tag.Name = body[:len(body)-len(closing)]
	if err := tag.Validate(); err != nil {
		return Tag{}, err
	}
	return tag, nil
}

// parseTagHead decodes the bytes between '<' and '>': either a bare type
// name, or `pii kind="..."`. Attributes on a non-pii type are refused
// rather than ignored, so an unexpected attribute cannot be smuggled
// through a parser that shrugs at it.
func parseTagHead(head string) (Tag, error) {
	if knownTagType(TagType(head)) && head != string(TagPII) {
		return Tag{Type: TagType(head)}, nil
	}
	const kindPrefix = string(TagPII) + ` kind="`
	if strings.HasPrefix(head, kindPrefix) && strings.HasSuffix(head, `"`) {
		kind := head[len(kindPrefix) : len(head)-1]
		return Tag{Type: TagPII, PIIKind: kind}, nil
	}
	return Tag{}, cascade.Newf(cascade.KindInvalidInput,
		"secrets: %q is not a tag type (want password, apikey, token, connstr, or pii with a kind attribute)",
		head)
}

// validateTagName enforces the NAME production, [A-Z][A-Z0-9_]*. It is
// stricter than validateSecretName, which also accepts lower case, '-'
// and '.': those characters are legal in a vault name but not inside a
// tag, where the parser must be able to tell a NAME from surrounding
// markup without ambiguity.
func validateTagName(name string) error {
	if name == "" {
		return cascade.New(cascade.KindInvalidInput, "secrets: a tag name must not be empty")
	}
	if len(name) > maxSecretNameLen {
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: a tag name is longer than the %d-character limit", maxSecretNameLen)
	}
	if name[0] < 'A' || name[0] > 'Z' {
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: tag name %q must start with an upper-case letter", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: tag name %q must match [A-Z][A-Z0-9_]*", name)
	}
	return nil
}

// knownTagType reports whether t is one of the five accepted types.
func knownTagType(t TagType) bool {
	for _, known := range tagTypes {
		if t == known {
			return true
		}
	}
	return false
}

// knownPIIKind reports whether kind is one of the three accepted kinds.
func knownPIIKind(kind string) bool {
	for _, known := range piiKinds {
		if kind == known {
			return true
		}
	}
	return false
}

// tagTypeForClass is the fixed detection-class to tag-type table. It is a
// slice of pairs rather than a map so the table reads in one order and
// cannot be iterated non-deterministically.
var tagTypeForClass = []struct {
	Class Class
	Type  TagType
}{
	{ClassAPIKey, TagAPIKey},
	{ClassBase64JSON, TagAPIKey},
	{ClassJWT, TagToken},
	{ClassBearer, TagToken},
	{ClassPEM, TagPassword},
	{ClassConnString, TagConnStr},
}

// TagFor maps a detection class to the tag type that stands in for it.
//
// The table is fixed and covers every class in the default registry:
// api-key and base64-json become apikey, jwt and bearer become token,
// pem-private-key becomes password, connection-string becomes connstr.
//
// Two classes deliberately have no row. ClassHighEntropy, the detector's
// shape-only signal, has no credential kind to name, so it returns an
// error and the rewriter refuses the turn rather than inventing a type
// for it. TagPII has no class at all: no detector in this phase emits a
// personal identifier, so <pii> tags are only ever hand-authored, and the
// grammar parses and preserves them so that they survive a round trip.
// Nothing in cascade claims to detect personal information.
//
// An unmapped class always returns an error and never a zero TagType: an
// empty type would render as a tag that parses back as something else.
func TagFor(class Class) (TagType, error) {
	for _, row := range tagTypeForClass {
		if row.Class == class {
			return row.Type, nil
		}
	}
	return "", cascade.Newf(cascade.KindInvalidInput,
		"secrets: detection class %q has no tag type; it cannot be rewritten into a tag", string(class))
}
