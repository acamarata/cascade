package memory

// Purpose: the value half of the on-disk codec: version gating and the
//   typed decode of every frontmatter value into a MemoryEntry. Split from
//   frontmatter.go per the 300-line file cap.
// Inputs: raw frontmatter lines and the parsed key/value map.
// Outputs: a fully-populated MemoryEntry, or a typed refusal.
// Constraints: every value is decoded with its declared type and refused
//   on mismatch; nothing is coerced, defaulted, or skipped. No I/O, no
//   clock.
// SPORT: G/memory-store (ADD, placeholder per T-1 sport_updates).

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// checkFormatVersion finds the format line and refuses anything this build
// cannot read. A missing format line is a malformed file; a well-formed
// line naming another version is an unsupported one, and the two are
// deliberately different errors.
func checkFormatVersion(header []string) error {
	for _, line := range header {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != keyFormat {
			continue
		}
		got, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
				"format value %q is not an integer", strings.TrimSpace(value))
		}
		if got != formatVersion {
			return cascade.Wrapf(cascade.KindUnsupported, ErrUnsupportedFormat,
				"record declares format %d, this build reads format %d", got, formatVersion)
		}
		return nil
	}
	return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
		"frontmatter is missing required key %q", keyFormat)
}

// buildEntry assembles the record from decoded values. Strings, the float
// and the timestamps are each decoded through their own typed helper, so a
// value of the wrong type is refused rather than coerced.
func buildEntry(fields map[string]string, body string) (MemoryEntry, error) {
	str, err := newStringDecoder(fields)
	if err != nil {
		return MemoryEntry{}, err
	}
	kind, err := ParseKind(str[keyKind])
	if err != nil {
		return MemoryEntry{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"record declares %v", err)
	}
	origin, err := ParseOrigin(str[keyOrigin])
	if err != nil {
		return MemoryEntry{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"record declares %v", err)
	}
	e := MemoryEntry{
		Name:        str[keyName],
		Kind:        kind,
		Description: str[keyDescription],
		Body:        body,
		ScopeRef:    str[keyScopeRef],
		CommitSHA:   str[keyCommitSHA],
		Supersedes:  str[keySupersedes],
		Provenance: Provenance{
			Origin:      origin,
			SessionID:   str[keySessionID],
			ContentHash: str[keyContentHash],
		},
	}
	if err := decodeInto(&e, str, fields); err != nil {
		return MemoryEntry{}, err
	}
	return e, nil
}

// decodeInto fills the non-string fields: the confidence float and the
// three timestamps.
func decodeInto(e *MemoryEntry, str map[string]string, fields map[string]string) error {
	conf, err := decodeFloat(fields[keyConfidence])
	if err != nil {
		return err
	}
	e.Confidence = conf
	created, err := decodeTime(keyCreatedAt, str[keyCreatedAt])
	if err != nil {
		return err
	}
	updated, err := decodeTime(keyUpdatedAt, str[keyUpdatedAt])
	if err != nil {
		return err
	}
	e.Provenance.CreatedAt = created
	e.Provenance.UpdatedAt = updated
	if raw := str[keyExpiresAt]; raw != "" {
		exp, expErr := decodeTime(keyExpiresAt, raw)
		if expErr != nil {
			return expErr
		}
		e.ExpiresAt = &exp
	}
	return nil
}

// stringKeys are the frontmatter keys whose values are JSON string
// literals. Listed explicitly rather than derived by subtraction so that
// adding a key without deciding its type is a compile-visible omission.
var stringKeys = []string{
	keyName, keyKind, keyDescription, keyScopeRef, keyCommitSHA,
	keySupersedes, keyExpiresAt, keyOrigin, keySessionID,
	keyCreatedAt, keyUpdatedAt, keyContentHash,
}

// newStringDecoder decodes every string-typed field at once, so a single
// wrongly-typed value (a bare scalar where a quoted string belongs, or a
// number) refuses the whole record rather than producing a struct with one
// silently empty field.
func newStringDecoder(fields map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(stringKeys))
	for _, k := range stringKeys {
		v, err := decodeString(k, fields[k])
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// decodeString parses one JSON string literal. A value that is not quoted
// is refused: unquoted scalars are exactly where YAML's type inference
// turns "no", "1.0" and "2026-01-01" into a boolean, a float and a date,
// and this format does not have that class of bug because it does not
// have unquoted scalars.
func decodeString(key, raw string) (string, error) {
	if !strings.HasPrefix(raw, `"`) {
		return "", cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"value of %q is not a quoted string: %q", key, raw)
	}
	var out string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return "", cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"value of %q is not a valid quoted string: %q", key, raw)
	}
	return out, nil
}

// decodeFloat parses the confidence value, which is written as a bare
// number and so must not be quoted.
func decodeFloat(raw string) (float64, error) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"value of %q is not a number: %q", keyConfidence, raw)
	}
	return v, nil
}

// decodeTime parses one timestamp and returns it in UTC, so a record reads
// back identically regardless of the reader's local time zone.
func decodeTime(key, raw string) (time.Time, error) {
	t, err := time.Parse(timeLayout, raw)
	if err != nil {
		return time.Time{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"value of %q is not an RFC3339 timestamp: %q", key, raw)
	}
	return t.UTC(), nil
}
