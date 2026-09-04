package memory

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestDecodeFailsClosed is the fail-closed table. Every input here is a
// file the store must refuse whole. None of them may come back as a
// partially populated record, and none may be repaired by guessing.
func TestDecodeFailsClosed(t *testing.T) {
	good := string(mustReadGolden(t, "entry_user.md"))
	header, body, _ := strings.Cut(good, "\n---\n")

	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"empty file", "", ErrMalformedEntry},
		{"no fence at all", "just a body\n", ErrMalformedEntry},
		{"fence not on first line", "\n" + good, ErrMalformedEntry},
		{"truncated mid header", header[:len(header)/2], ErrMalformedEntry},
		{"header with no closing fence", header + "\n", ErrMalformedEntry},
		{"opening fence only", "---\n", ErrMalformedEntry},
		{"opening fence and body, no close", "---\nname: \"x\"\n" + body, ErrMalformedEntry},
		{"line that is not a pair", strings.Replace(good, "name: \"units-and-clock\"", "not a pair", 1), ErrMalformedEntry},
		{"unknown key", strings.Replace(good, "commit_sha:", "commit_shaa:", 1), ErrMalformedEntry},
		{"duplicate key", strings.Replace(good, "name: \"units-and-clock\"", "name: \"a\"\nname: \"b\"", 1), ErrMalformedEntry},
		{"missing key", strings.Replace(good, "confidence: 0.9\n", "", 1), ErrMalformedEntry},
		{"missing format key", strings.Replace(good, "format: 1\n", "", 1), ErrMalformedEntry},
		{"unquoted string value", strings.Replace(good, `name: "units-and-clock"`, "name: units-and-clock", 1), ErrMalformedEntry},
		{"number where string belongs", strings.Replace(good, `name: "units-and-clock"`, "name: 42", 1), ErrMalformedEntry},
		{"unterminated quoted string", strings.Replace(good, `name: "units-and-clock"`, `name: "unterminated`, 1), ErrMalformedEntry},
		{"string where number belongs", strings.Replace(good, "confidence: 0.9", `confidence: "high"`, 1), ErrMalformedEntry},
		{"quoted format version", strings.Replace(good, "format: 1", `format: "1"`, 1), ErrMalformedEntry},
		{"timestamp that is not RFC3339", strings.Replace(good, `created_at: "2026-01-02T03:04:05Z"`, `created_at: "yesterday"`, 1), ErrMalformedEntry},
		{"ttl that is not RFC3339", strings.Replace(good, `expires_at: ""`, `expires_at: "soon"`, 1), ErrMalformedEntry},
		{"future format version", strings.Replace(good, "format: 1", "format: 2", 1), ErrUnsupportedFormat},
		{"format version zero", strings.Replace(good, "format: 1", "format: 0", 1), ErrUnsupportedFormat},
		{"non-numeric format version", strings.Replace(good, "format: 1", "format: v1", 1), ErrMalformedEntry},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodeEntry([]byte(c.input))
			if !errors.Is(err, c.want) {
				t.Fatalf("error = %v, want %v", err, c.want)
			}
			if got != (MemoryEntry{}) {
				t.Fatalf("a refused record came back populated: %+v", got)
			}
		})
	}
}

// TestDecodeErrorsCarryTheRightKind pins the taxonomy classification, so a
// damaged record and a record from a newer build are distinguishable by
// kind and not only by sentinel.
func TestDecodeErrorsCarryTheRightKind(t *testing.T) {
	good := string(mustReadGolden(t, "entry_user.md"))
	_, err := decodeEntry([]byte("garbage"))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("malformed record error kind = %v, want integrity", err)
	}
	_, err = decodeEntry([]byte(strings.Replace(good, "format: 1", "format: 7", 1)))
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("future-format error kind = %v, want unsupported", err)
	}
}

// TestDecodeToleratesCRLFHeader proves a checkout that translated line
// endings can still be read, which matters because the fixtures and the
// records themselves are text files on platforms that do that.
func TestDecodeToleratesCRLFHeader(t *testing.T) {
	good := string(mustReadGolden(t, "entry_user.md"))
	header, body, _ := strings.Cut(good, "---\nPrefers")
	crlf := strings.ReplaceAll(header, "\n", "\r\n") + "---\r\nPrefers" + body
	got, err := decodeEntry([]byte(crlf))
	if err != nil {
		t.Fatalf("a CRLF header was refused: %v", err)
	}
	if got.Name != "units-and-clock" {
		t.Errorf("name = %q after CRLF decode", got.Name)
	}
}
