package runtime

// Purpose: canonicalLiteral — the ONE literal validator both
// ConfigWriter.Set and ConfigWriter.ApplyDiff use before writing a value
// (P1-E25-W5-S103-T1 rework, T0 decision N2; fixes PCI
// config-set-literal-injects-tables). The former ParseTomlLiteral decoded
// "v = <lit>" and kept only v, so a literal with trailing text
// ("\"a\"\n[agents.egress]\nallowlist = [\"*\"]") passed it, and both
// writers then put the caller's RAW text on the value side of a line:
// one entry could add any table to config.toml.
// Inputs: a caller-supplied TOML literal string.
// Outputs: the decoded value and its canonical single-line re-encoding,
// which is the only text a writer may put after "key =".
// Constraints: the literal must decode to exactly one value (one key "v"
// and nothing else in the document), and the caller's text is never
// written: encodeTomlValue re-encodes the decoded value and escapes every
// control character, so no newline can reach the file. A value whose
// re-encoding does not decode back to an equal value is refused.
// SPORT: internal/runtime config_literal.go (ADD) — P1-E25-W5-S103-T1.

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// literalHint is the format hint every refused literal carries.
const literalHint = "not a valid TOML literal (bool/int/float/quoted string/array/inline table); wrap bare strings in double quotes"

// canonicalLiteral validates raw as exactly one TOML value and returns the
// value with its canonical encoding. Every refusal is a *LiteralError.
func canonicalLiteral(raw string) (interface{}, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, "", &LiteralError{Raw: raw, Hint: "empty value; " + literalHint}
	}
	value, err := ParseTomlLiteral(trimmed)
	if err != nil {
		return nil, "", &LiteralError{Raw: raw, Hint: err.Error()}
	}
	canonical, err := encodeTomlValue(value)
	if err != nil {
		return nil, "", &LiteralError{Raw: raw, Hint: err.Error()}
	}
	back, err := ParseTomlLiteral(canonical)
	if err != nil || !reflect.DeepEqual(back, value) {
		return nil, "", &LiteralError{Raw: raw, Hint: "the value does not survive a canonical re-encoding"}
	}
	return value, canonical, nil
}

// encodeTomlValue renders a decoded TOML value as one line of TOML.
func encodeTomlValue(v interface{}) (string, error) {
	switch x := v.(type) {
	case string:
		return tomlBasicString(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return canonicalFloat(x), nil
	case []interface{}:
		return encodeTomlArray(x)
	case map[string]interface{}:
		return encodeInlineTable(x)
	case time.Time:
		return x.Format(time.RFC3339Nano), nil
	case toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return fmt.Sprint(x), nil
	}
	return "", fmt.Errorf("unsupported TOML value type %T", v)
}

// tomlBasicString quotes s as a TOML basic string, escaping every control
// character (TOML forbids them raw; a raw newline would end the line).
func tomlBasicString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// canonicalFloat renders f so it decodes back as a float, never an integer.
func canonicalFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// encodeTomlArray renders a one-line array of canonical elements.
func encodeTomlArray(items []interface{}) (string, error) {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		s, err := encodeTomlValue(item)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	return "[" + strings.Join(parts, ", ") + "]", nil
}

// encodeInlineTable renders a one-line inline table, keys sorted so the
// same table always encodes to the same bytes (the idempotent re-run
// depends on it). A key that is not a bare key is quoted.
func encodeInlineTable(table map[string]interface{}) (string, error) {
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		s, err := encodeTomlValue(table[k])
		if err != nil {
			return "", err
		}
		key := k
		if !bareKeyPattern.MatchString(k) {
			key = tomlBasicString(k)
		}
		parts = append(parts, key+" = "+s)
	}
	return "{" + strings.Join(parts, ", ") + "}", nil
}
