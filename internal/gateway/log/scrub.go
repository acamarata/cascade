// Package log provides gateway logging utilities including key scrubbing.
package log

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ScrubbedKey holds four replacement forms of a pool key for scrubbing logs.
// The fields are ordered longest first so that replacing in field order
// ensures no fragment of the original key remains.
type ScrubbedKey struct {
	// Raw is the literal replacement [key].
	Raw string
	// GoQuoted is the Go-quoted replacement via strconv.Quote.
	GoQuoted string
	// JSON is the JSON-encoded replacement via json.Marshal.
	JSON string
	// NoHTMLEscape is the JSON-encoded replacement with HTML escaping disabled.
	NoHTMLEscape string
}

// ScrubForms generates four scrubbed replacement forms for the given key.
// It returns an error if the key contains a quote, backslash, or control character.
// The returned struct fields are ordered longest first so that no fragment of the key remains.
func ScrubForms(key string) (ScrubbedKey, error) {
	for _, r := range key {
		if r == '"' || r == '\\' || (r >= 0x00 && r <= 0x1f) || r == 0x7f {
			return ScrubbedKey{}, fmt.Errorf("key contains invalid character: %U", r)
		}
	}

	raw := "[" + key + "]"
	goQuoted := strconv.Quote(raw)

	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return ScrubbedKey{}, fmt.Errorf("json marshal: %w", err)
	}
	jsonStr := string(jsonBytes)

	noHTML, err := encodeNoHTMLEscape(raw)
	if err != nil {
		return ScrubbedKey{}, err
	}

	type formEntry struct {
		val  string
		name string
	}
	forms := []formEntry{
		{raw, "Raw"},
		{goQuoted, "GoQuoted"},
		{jsonStr, "JSON"},
		{noHTML, "NoHTMLEscape"},
	}
	sort.SliceStable(forms, func(i, j int) bool {
		return len(forms[i].val) > len(forms[j].val)
	})

	var result ScrubbedKey
	for i, f := range forms {
		switch i {
		case 0:
			result.Raw = f.val
		case 1:
			result.GoQuoted = f.val
		case 2:
			result.JSON = f.val
		case 3:
			result.NoHTMLEscape = f.val
		}
	}

	return result, nil
}

// encodeNoHTMLEscape JSON-encodes s with HTML escaping disabled and without
// the trailing newline json.Encoder.Encode appends.
func encodeNoHTMLEscape(s string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", fmt.Errorf("json encode: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
