//go:build !windows

package main

import (
	"reflect"
	"testing"
)

// TestCIScopePrefixes pins how a job's mutable scope becomes the
// dispatcher's scope prefixes: a literal path covers itself, "dir/**" covers
// "dir/"; a whole-repo "**" yields no prefix (its paths fail closed as out
// of scope); any other glob form is refused by jobs.NormalizeScope.
func TestCIScopePrefixes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"src/**", []string{"src/"}},
		{"docs/**,src/main.go", []string{"docs/", "src/main.go"}},
		{"src/main.go", []string{"src/main.go"}},
		{"src/a.go,src/b.go", []string{"src/a.go", "src/b.go"}},
		{"**", nil},
	} {
		got, err := scopePrefixes(tc.in)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("scopePrefixes(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"../escape/**", "src/*.go"} {
		if _, err := scopePrefixes(bad); err == nil {
			t.Errorf("scopePrefixes(%q) succeeded, want the malformed-scope refusal", bad)
		}
	}
}

// TestCIExactLiterals: a changed path that only starts with a literal scope
// entry drops the literal prefixes (fail closed); exact matches keep them.
func TestCIExactLiterals(t *testing.T) {
	prefixes := []string{"docs/", "src/a.go", "lib/b.go"}
	if got := exactLiterals([]string{"src/a.go", "docs/x.md"}, prefixes); !reflect.DeepEqual(got, prefixes) {
		t.Errorf("exact match: %q, want %q", got, prefixes)
	}
	for _, widened := range []string{"src/a.go.orig", "src/a.go/x"} {
		if got := exactLiterals([]string{"src/a.go", widened}, prefixes); !reflect.DeepEqual(got, []string{"docs/"}) {
			t.Errorf("%s: %q, want only the recursive prefix [docs/]", widened, got)
		}
	}
}
