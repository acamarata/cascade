package secrets

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseVaultEnvAccepts(t *testing.T) {
	input := strings.Join([]string{
		"# a comment",
		"",
		"   ",
		"PLAIN=value",
		"export EXPORTED=value2",
		`QUOTED="hello world"`,
		`ESCAPED="line\none\ttab\\slash\"quote"`,
		"SINGLE='raw \\n stays'",
		"EMPTY=",
		"SPACED  =  spaced-value",
		"EQUALS=a=b=c",
		"DUP=first",
		"DUP=second",
	}, "\n")
	entries, err := ParseVaultEnv([]byte(input))
	if err != nil {
		t.Fatalf("ParseVaultEnv: %v", err)
	}
	want := []struct {
		name  string
		value string
	}{
		{"PLAIN", "value"},
		{"EXPORTED", "value2"},
		{"QUOTED", "hello world"},
		{"ESCAPED", "line\none\ttab\\slash\"quote"},
		{"SINGLE", `raw \n stays`},
		{"EMPTY", ""},
		{"SPACED", "spaced-value"},
		{"EQUALS", "a=b=c"},
		{"DUP", "first"},
		{"DUP", "second"},
	}
	if len(entries) != len(want) {
		t.Fatalf("parsed %d entries, want %d: %v", len(entries), len(want), names(entries))
	}
	for i, w := range want {
		if entries[i].Name != w.name || string(entries[i].Value) != w.value {
			t.Fatalf("entry %d = %q/%q, want %q/%q", i, entries[i].Name, entries[i].Value, w.name, w.value)
		}
		if entries[i].Line == 0 {
			t.Fatalf("entry %d carries no line number", i)
		}
	}
}

func names(entries []EnvEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func TestParseVaultEnvRefusals(t *testing.T) {
	cases := map[string]string{
		"no equals":          "JUST_A_WORD",
		"bad key":            "not a key=value",
		"empty key":          "=value",
		"embedded nul":       "KEY=va\x00lue",
		"unterminated quote": `KEY="unterminated`,
		"single unbalanced":  `KEY='a'b'`,
		"over-long line":     "KEY=" + strings.Repeat("x", maxEnvLineLen+10),
	}
	for label, input := range cases {
		_, err := ParseVaultEnv([]byte(input))
		if !isKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s was accepted: %v", label, err)
		}
	}
}

// TestParseVaultEnvErrorsWithholdContent is the redaction assertion for the
// parser: a refusal names the line NUMBER and never the line, because the
// line is a secret.
func TestParseVaultEnvErrorsWithholdContent(t *testing.T) {
	const canary = "canary-secret-value"
	inputs := []string{
		"GOOD=ok\nKEY=\"" + canary,
		"GOOD=ok\nbad key=" + canary,
		"GOOD=ok\nKEY=" + canary + "\x00",
		"GOOD=ok\n" + canary,
	}
	for _, input := range inputs {
		_, err := ParseVaultEnv([]byte(input))
		if err == nil {
			t.Fatalf("input %q was accepted", input)
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("the parse error echoed the value: %v", err)
		}
		if !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("the parse error does not locate the line: %v", err)
		}
	}
}

func TestParseVaultEnvCRLF(t *testing.T) {
	entries, err := ParseVaultEnv([]byte("A=1\r\nB=2\r\n"))
	if err != nil {
		t.Fatalf("CRLF input refused: %v", err)
	}
	if len(entries) != 2 || string(entries[0].Value) != "1" || string(entries[1].Value) != "2" {
		t.Fatalf("CRLF parse = %v", entries)
	}
}

func TestVaultImportIdempotent(t *testing.T) {
	b, custody := newTestBroker(t, &allowGate{})
	ctx := context.Background()
	data := []byte("A=1\nB=2\n# comment\nA=3\n")

	first, err := Import(ctx, b, data)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Parsed != 3 || first.Created != 2 || first.Updated != 0 {
		t.Fatalf("first import report = %+v", first)
	}
	if strings.Join(first.Names, ",") != "A,B" {
		t.Fatalf("first import names = %v", first.Names)
	}

	second, err := Import(ctx, b, data)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Updated != 2 || second.Created != 0 {
		t.Fatalf("second import report = %+v", second)
	}
	if len(custody.entries) != 2 {
		t.Fatalf("import produced %d entries, want 2", len(custody.entries))
	}
	if string(custody.entries["A"]) != "3" {
		t.Fatal("the last value for a duplicated key did not win")
	}
}

func TestImportRefusals(t *testing.T) {
	ctx := context.Background()
	if _, err := Import(ctx, nil, []byte("A=1")); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("import with no broker = %v", err)
	}
	b, custody := newTestBroker(t, &allowGate{})
	if _, err := Import(ctx, b, []byte("A=1\nnot-an-assignment\n")); !isKind(err, cascade.KindInvalidInput) {
		t.Fatalf("a malformed file was accepted: %v", err)
	}
	if len(custody.entries) != 0 {
		t.Fatal("a refused import left entries behind: parsing must complete before the first write")
	}
	custody.failOn = "set"
	custody.err = ErrCustodyUnavailable("memory", nil)
	if _, err := Import(ctx, b, []byte("A=1")); !isKind(err, cascade.KindUnavailable) {
		t.Fatalf("a failing store was not surfaced: %v", err)
	}
}

// TestImportNeverEchoesValues covers the whole import path: neither the
// report nor any error may carry a value.
func TestImportNeverEchoesValues(t *testing.T) {
	b, _ := newTestBroker(t, &allowGate{})
	const canary = "canary-secret-value"
	report, err := Import(context.Background(), b, []byte("A="+canary+"\n"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	rendered := strings.Join(append(report.Names, "parsed"), " ")
	if strings.Contains(rendered, canary) {
		t.Fatal("the import report carries the value")
	}
}

func TestUnescapeDoubleQuoted(t *testing.T) {
	cases := map[string]string{
		`plain`:     "plain",
		`a\nb`:      "a\nb",
		`a\rb`:      "a\rb",
		`a\tb`:      "a\tb",
		`a\\b`:      `a\b`,
		`a\"b`:      `a"b`,
		`a\qb`:      `a\qb`,
		`trailing\`: `trailing\`,
	}
	for in, want := range cases {
		if got := unescapeDoubleQuoted(in); got != want {
			t.Fatalf("unescapeDoubleQuoted(%q) = %q, want %q", in, got, want)
		}
	}
}
