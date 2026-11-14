package crunch

import (
	"errors"
	"reflect"
	"testing"
)

// checkCoreArgsError asserts that ExtractCoreArgs fails on args with the
// sentinel want and its literal code, returns zero CoreArgs and leaves the
// input slice unchanged.
func checkCoreArgsError(t *testing.T, args []string, want error, code string) {
	t.Helper()
	orig := append([]string(nil), args...)
	got, err := ExtractCoreArgs(args)
	if !errors.Is(err, want) {
		t.Fatalf("ExtractCoreArgs(%q) error = %v, want %v", args, err, want)
	}
	if err.Error() != code {
		t.Errorf("ExtractCoreArgs(%q) Error() = %q, want %q", args, err.Error(), code)
	}
	if !reflect.DeepEqual(got, CoreArgs{}) {
		t.Errorf("ExtractCoreArgs(%q) = %+v, want zero CoreArgs", args, got)
	}
	if !reflect.DeepEqual(args, orig) {
		t.Errorf("input mutated: got %q, want %q", args, orig)
	}
}

// TestExtractCoreArgsInvalidFlags covers syntax errors that return
// ErrInvalidFlags with code "1".
func TestExtractCoreArgsInvalidFlags(t *testing.T) {
	cases := map[string][]string{
		"unknown flag":        {"--dir", "/w", "--prompt", "p", "--bogus", "x"},
		"unknown eq flag":     {"--dir=/w", "--prompt=p", "--bogus=x"},
		"positional":          {"--dir", "/w", "stray", "--prompt", "p"},
		"leading positional":  {"stray", "--dir", "/w", "--prompt", "p"},
		"single dash":         {"-dir", "/w", "--prompt", "p"},
		"bare double dash":    {"--dir", "/w", "--prompt", "p", "--"},
		"empty flag name":     {"--=x", "--dir", "/w", "--prompt", "p"},
		"repeated dir":        {"--dir", "/a", "--dir", "/b", "--prompt", "p"},
		"repeated prompt":     {"--dir", "/a", "--prompt", "p", "--prompt=q"},
		"repeated verify":     {"--dir", "/a", "--prompt", "p", "--verify=x", "--verify", "x"},
		"duplicate scope":     {"--dir", "/a", "--prompt", "p", "--scope", "a.go", "--scope=a.go"},
		"invalid before miss": {"--bogus", "x"},
		"dup scope no dir":    {"--scope", "a.go", "--scope", "a.go"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			checkCoreArgsError(t, args, ErrInvalidFlags, "1")
		})
	}
}

// TestExtractCoreArgsMissingRequired covers absent required flags and
// missing or blank values that return ErrMissingRequired with code "2".
func TestExtractCoreArgsMissingRequired(t *testing.T) {
	cases := map[string][]string{
		"no dir":               {"--prompt", "p"},
		"no prompt":            {"--dir", "/w"},
		"only verify":          {"--verify", "true"},
		"only scope":           {"--scope", "a.go"},
		"dir value at end":     {"--prompt", "p", "--dir"},
		"dir eq empty":         {"--dir=", "--prompt", "p"},
		"prompt eq empty":      {"--dir", "/w", "--prompt="},
		"next token is flag":   {"--dir", "--prompt", "p"},
		"verify next flag":     {"--dir", "/w", "--verify", "--prompt", "p"},
		"scope next flag":      {"--dir", "/w", "--scope", "--prompt", "p"},
		"whitespace dir":       {"--dir", "  ", "--prompt", "p"},
		"whitespace prompt":    {"--dir", "/w", "--prompt=\t "},
		"whitespace verify":    {"--dir", "/w", "--prompt", "p", "--verify", " "},
		"whitespace scope":     {"--dir", "/w", "--prompt", "p", "--scope= "},
		"missing before dup":   {"--dir", "", "--dir", "/w", "--prompt", "p"},
		"missing before bogus": {"--dir=", "--bogus", "x"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			checkCoreArgsError(t, args, ErrMissingRequired, "2")
		})
	}
}
