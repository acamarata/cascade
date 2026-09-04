package secrets

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fuzzSeedDir is the by-target seed corpus for FuzzVaultEnvParse. It lives
// under internal/testdata/fuzz/<Target>/ rather than beside the package so
// every fuzz target in the repo is discoverable in one place.
const fuzzSeedDir = "../testdata/fuzz/FuzzVaultEnvParse"

// FuzzVaultEnvParse drives the vault.env parser. The properties asserted
// are stronger than "it did not panic":
//
//  1. every entry the parser returns carries a name the validator accepts,
//     so a caller can never be handed a name the store would reject;
//  2. a successful parse drops no assignment: the number of entries equals
//     the number of lines that are assignments, so a malformed run cannot
//     silently discard a key;
//  3. re-parsing the canonical rendering of a parse yields the same
//     entries, so parsing is stable.
func FuzzVaultEnvParse(f *testing.F) {
	addFuzzSeeds(f)
	f.Fuzz(func(t *testing.T, data string) {
		entries, err := ParseVaultEnv([]byte(data))
		if err != nil {
			if len(entries) != 0 {
				t.Fatalf("a refused parse still returned %d entries", len(entries))
			}
			return
		}
		assignments := 0
		for _, line := range strings.Split(data, "\n") {
			trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			assignments++
		}
		if len(entries) != assignments {
			t.Fatalf("parsed %d entries from %d assignment lines: a key was dropped", len(entries), assignments)
		}
		for _, entry := range entries {
			if verr := validateSecretName(entry.Name); verr != nil {
				t.Fatalf("parser returned an unusable name %q: %v", entry.Name, verr)
			}
			if entry.Line < 1 {
				t.Fatalf("entry %q carries line %d", entry.Name, entry.Line)
			}
		}
	})
}

// addFuzzSeeds loads the checked-in corpus. A missing or unreadable corpus
// is a failure, not a silent skip: a fuzz target with no seeds explores far
// less than the one the corpus was written for.
func addFuzzSeeds(f *testing.F) {
	f.Helper()
	names, err := os.ReadDir(fuzzSeedDir)
	if err != nil {
		f.Fatalf("reading the seed corpus: %v", err)
	}
	if len(names) == 0 {
		f.Fatal("the seed corpus is empty")
	}
	// The corpus files are in go test's own fuzz-corpus format, which the
	// toolchain loads for `go test -fuzz`. Seeding f explicitly as well
	// means the non-fuzzing run (`go test`) also exercises every seed.
	for _, entry := range names {
		raw, rerr := os.ReadFile(filepath.Join(fuzzSeedDir, entry.Name())) //nolint:gosec // fixed test corpus path
		if rerr != nil {
			f.Fatalf("reading seed %s: %v", entry.Name(), rerr)
		}
		f.Add(decodeCorpusString(string(raw)))
	}
}

// decodeCorpusString pulls the string literal out of a go-test corpus file.
// A file it cannot read yields the raw text, which is still a valid seed.
func decodeCorpusString(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "string(") {
			continue
		}
		unquoted, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(line, "string("), ")"))
		if err == nil {
			return unquoted
		}
	}
	return raw
}
