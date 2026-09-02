// Purpose: fuzz the TOML manifest decoder only, per T0 ruling R-14.10
// ("plugin manifest is TOML-ONLY ... FuzzParseManifest fuzzes the TOML
// decoder only"). Seeds from the three golden fixtures physically copied to
// internal/testdata/fuzz/manifest/ (06-FORGE-SPEC.md §5.7: fuzz corpora
// live under internal/testdata/fuzz/, never beside the package).
// Constraints: no network calls (Art.7.2); reads only, no writes.
package plugin_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// fuzzManifestCorpusDir is the shared fuzz-corpus home for
// FuzzParseManifest, relative to this package's directory
// (pkg/plugin -> repo root is two levels up).
const fuzzManifestCorpusDir = "../../internal/testdata/fuzz/manifest"

// loadFuzzSeeds reads every *.toml file in fuzzManifestCorpusDir, sorted for
// determinism, and returns their contents as strings.
func loadFuzzSeeds(t *testing.F) []string {
	t.Helper()
	entries, err := os.ReadDir(fuzzManifestCorpusDir)
	if err != nil {
		t.Fatalf("reading fuzz corpus dir %s: %v", fuzzManifestCorpusDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	seeds := make([]string, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(fuzzManifestCorpusDir, name))
		if err != nil {
			t.Fatalf("reading fuzz seed %s: %v", name, err)
		}
		seeds = append(seeds, string(data))
	}
	if len(seeds) == 0 {
		t.Fatalf("fuzz corpus dir %s has no .toml seeds", fuzzManifestCorpusDir)
	}
	return seeds
}

// FuzzParseManifest fuzzes plugin.ParseManifest's TOML decoding path only
// (R-14.10): it must never panic, and it must uphold the fail-closed
// invariant (a non-nil error is never accompanied by a non-zero Manifest)
// for arbitrary byte input, valid or not.
func FuzzParseManifest(f *testing.F) {
	for _, seed := range loadFuzzSeeds(f) {
		f.Add(seed)
	}
	// A handful of adversarial non-seed inputs, to give the fuzzer
	// interesting starting points beyond the three fully-valid goldens.
	f.Add("")
	f.Add("id = \"x\"")
	f.Add("[[provides.commands]]\nname = \"config\"\n")

	f.Fuzz(func(t *testing.T, data string) {
		m, err := plugin.ParseManifest(strings.NewReader(data))
		if err != nil {
			if !reflect.DeepEqual(m, plugin.Manifest{}) {
				t.Fatalf("ParseManifest: got non-zero Manifest %+v alongside error %v (fail-closed violation)", m, err)
			}
			return
		}
		// err == nil means m must already satisfy Validate with zero
		// findings — ParseManifest validates internally before returning.
		if errs := plugin.Validate(m); len(errs) != 0 {
			t.Fatalf("ParseManifest returned nil error but Validate(m) found %d issues: %+v", len(errs), errs)
		}
	})
}
