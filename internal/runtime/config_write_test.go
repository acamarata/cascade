package runtime

import (
	"strings"
	"testing"
)

func TestSplitDottedPath_Valid(t *testing.T) {
	segs, err := SplitDottedPath("retrieval.fusion.k")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"retrieval", "fusion", "k"}
	if len(segs) != len(want) {
		t.Fatalf("got %v want %v", segs, want)
	}
	for i := range want {
		if segs[i] != want[i] {
			t.Fatalf("got %v want %v", segs, want)
		}
	}
}

func TestSplitDottedPath_Invalid(t *testing.T) {
	cases := []string{"", "a..b", ".a", "a.", "a b.c", "a$.b"}
	for _, c := range cases {
		if _, err := SplitDottedPath(c); err == nil {
			t.Errorf("SplitDottedPath(%q): expected error, got nil", c)
		}
	}
}

func TestResolveDottedPath_KnownKeyOK(t *testing.T) {
	if _, err := ResolveDottedPath("retrieval.fusion.k"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDottedPath_UnknownSuggestsNearest(t *testing.T) {
	_, err := ResolveDottedPath("retrieval.fusion.kk")
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	var dpe *DottedPathError
	if e, ok := err.(*DottedPathError); ok {
		dpe = e
	} else {
		t.Fatalf("expected *DottedPathError, got %T", err)
	}
	if dpe.Suggestion != "retrieval.fusion.k" {
		t.Fatalf("expected suggestion retrieval.fusion.k, got %q", dpe.Suggestion)
	}
}

func TestResolveDottedPath_TotallyUnknownStillSuggestsSomething(t *testing.T) {
	_, err := ResolveDottedPath("zzz.nonexistent.key")
	if err == nil {
		t.Fatal("expected error")
	}
	dpe := err.(*DottedPathError)
	if dpe.Suggestion == "" {
		t.Fatal("expected a non-empty nearest-match suggestion")
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"kitten", "sitting", 3},
		{"retrieval.fusion.k", "retrieval.fusion.kk", 1},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseTomlLiteral_Valid(t *testing.T) {
	cases := []struct {
		raw  string
		want interface{}
	}{
		{"true", true},
		{"false", false},
		{"42", int64(42)},
		{`"hello"`, "hello"},
	}
	for _, c := range cases {
		got, err := ParseTomlLiteral(c.raw)
		if err != nil {
			t.Fatalf("ParseTomlLiteral(%q): unexpected error: %v", c.raw, err)
		}
		if got != c.want {
			t.Errorf("ParseTomlLiteral(%q) = %#v, want %#v", c.raw, got, c.want)
		}
	}
}

func TestParseTomlLiteral_Float(t *testing.T) {
	got, err := ParseTomlLiteral("1.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f, ok := got.(float64); !ok || f != 1.5 {
		t.Fatalf("got %#v, want float64(1.5)", got)
	}
}

func TestParseTomlLiteral_Array(t *testing.T) {
	got, err := ParseTomlLiteral(`["a","b"]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	arr, ok := got.([]interface{})
	if !ok || len(arr) != 2 || arr[0] != "a" || arr[1] != "b" {
		t.Fatalf("got %#v", got)
	}
}

func TestParseTomlLiteral_InvalidReturnsHint(t *testing.T) {
	cases := []string{"", "bareword", "{unclosed", "[1,2"}
	for _, c := range cases {
		_, err := ParseTomlLiteral(c)
		if err == nil {
			t.Errorf("ParseTomlLiteral(%q): expected error, got nil", c)
			continue
		}
		var le *LiteralError
		if e, ok := err.(*LiteralError); ok {
			le = e
		} else {
			t.Errorf("ParseTomlLiteral(%q): expected *LiteralError, got %T", c, err)
			continue
		}
		if le.Hint == "" {
			t.Errorf("ParseTomlLiteral(%q): expected non-empty format hint", c)
		}
	}
}

func TestLooksLikeSecret_BearerPrefixes(t *testing.T) {
	cases := []string{
		"sk-abcdefghijklmnopqrstuvwxyz0123456789",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"xoxb" + "-1234567890-" + "abcdefghijklmnop",
		"ya29.a0AfH6SMBexampletoken1234567890",
	}
	for _, c := range cases {
		if bad, reason := LooksLikeSecret(c); !bad {
			t.Errorf("LooksLikeSecret(%q) = false, want true", c)
		} else if reason == "" {
			t.Errorf("LooksLikeSecret(%q): empty reason", c)
		}
	}
}

func TestLooksLikeSecret_PEMHeader(t *testing.T) {
	if bad, _ := LooksLikeSecret("-----BEGIN PRIVATE KEY-----"); !bad {
		t.Fatal("expected PEM header to be detected as secret")
	}
}

func TestLooksLikeSecret_BareBase64(t *testing.T) {
	token := strings.Repeat("aB3", 20) // 60 chars, no whitespace
	if bad, _ := LooksLikeSecret(token); !bad {
		t.Fatalf("expected bare 60-char token to be detected as secret")
	}
}

func TestLooksLikeSecret_NegativeCases(t *testing.T) {
	cases := []string{"", "info", "json", "debug", "a short string", "true", "80"}
	for _, c := range cases {
		if bad, reason := LooksLikeSecret(c); bad {
			t.Errorf("LooksLikeSecret(%q) = true (%s), want false", c, reason)
		}
	}
}

func TestLooksLikeSecret_ProseWithHighEntropySubstringNotFlagged(t *testing.T) {
	// A long prose sentence (whitespace-containing, no bearer prefix, no
	// PEM header) is never flagged by the bare-base64 heuristic, even
	// though it happens to be long.
	s := "this is a perfectly ordinary sentence describing a config value in plain english words"
	if bad, reason := LooksLikeSecret(s); bad {
		t.Fatalf("prose should never be flagged as secret-shaped: %q (%s)", s, reason)
	}
}

func TestLooksLikeSecret_BearerPrefixFlaggedEvenWithTrailingProse(t *testing.T) {
	// A bearer-token prefix is a strong enough signal that it is flagged
	// unconditionally, even if the rest of the string contains spaces —
	// a real "sk-..." key never legitimately has spaces after it, so
	// this is still the safe (redirect-to-vault) default.
	s := "sk-abcdefghijklmnopqrstuvwxyz with trailing words"
	if bad, _ := LooksLikeSecret(s); !bad {
		t.Fatalf("expected bearer-prefixed value to be flagged regardless of trailing content: %q", s)
	}
}

func TestValidate_ValidTreePasses(t *testing.T) {
	tree := map[string]interface{}{
		"schema_version": int64(1),
		"elevation":      map[string]interface{}{"allow_remote": false},
		"logging":        map[string]interface{}{"level": "info"},
	}
	if err := Validate(tree); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_InvalidElevationFails(t *testing.T) {
	tree := map[string]interface{}{
		"elevation": map[string]interface{}{"allow_remote": true}, // missing helper_pubkey
	}
	if err := Validate(tree); err == nil {
		t.Fatal("expected error for allow_remote=true without helper_pubkey")
	}
}

func TestValidate_InvalidLoggingLevelFails(t *testing.T) {
	tree := map[string]interface{}{
		"logging": map[string]interface{}{"level": "not-a-level"},
	}
	if err := Validate(tree); err == nil {
		t.Fatal("expected error for invalid logging level")
	}
}

func TestValidate_NewerSchemaVersionFails(t *testing.T) {
	tree := map[string]interface{}{"schema_version": int64(CurrentSchemaVersion + 1)}
	err := Validate(tree)
	if err == nil {
		t.Fatal("expected error for schema_version newer than supported")
	}
	if _, ok := err.(*SchemaError); !ok {
		t.Fatalf("expected *SchemaError, got %T", err)
	}
}

func TestSortedKnownKeys_Sorted(t *testing.T) {
	keys := sortedKnownKeys()
	for i := 1; i < len(keys); i++ {
		if keys[i-1] > keys[i] {
			t.Fatalf("not sorted at index %d: %q > %q", i, keys[i-1], keys[i])
		}
	}
}
