// Purpose: FuzzEventDecode (task 7) — proves decodeEvent never panics for
//
//	any input, well-formed or adversarial. Seeds from the hand-curated
//	corpus at internal/testdata/fuzz/events/ (06-FORGE-SPEC.md §5.7: fuzz
//	corpora live under internal/testdata/fuzz/, never beside the owning
//	package — mirrors pkg/plugin/fuzz_test.go's loadFuzzSeeds pattern).
//
// Constraints: no network calls (Art.7.2); reads only.
// SPORT: internal.events.Event/ADDED (FuzzEventDecode) (P1-E03-W1-S04-T3).
package events

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fuzzEventCorpusDir is the shared fuzz-corpus home for FuzzEventDecode,
// relative to this package's directory (internal/events -> repo root is
// two levels up).
const fuzzEventCorpusDir = "../../internal/testdata/fuzz/events"

// loadFuzzEventSeeds reads every *.bin file in fuzzEventCorpusDir, sorted
// for determinism, and returns their raw bytes.
func loadFuzzEventSeeds(t *testing.F) [][]byte {
	t.Helper()
	entries, err := os.ReadDir(fuzzEventCorpusDir)
	if err != nil {
		t.Fatalf("reading fuzz corpus dir %s: %v", fuzzEventCorpusDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".bin") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	seeds := make([][]byte, 0, len(names))
	for _, name := range names {
		data, rerr := os.ReadFile(filepath.Join(fuzzEventCorpusDir, name))
		if rerr != nil {
			t.Fatalf("reading fuzz seed %s: %v", name, rerr)
		}
		seeds = append(seeds, data)
	}
	if len(seeds) == 0 {
		t.Fatalf("fuzz corpus dir %s has no .bin seeds", fuzzEventCorpusDir)
	}
	return seeds
}

// FuzzEventDecode proves decodeEvent never panics on arbitrary bytes, and
// that a successfully decoded envelope re-encodes and re-decodes
// identically (idempotence — the same property FuzzTomlLiteral checks for
// the config parser).
func FuzzEventDecode(f *testing.F) {
	for _, seed := range loadFuzzEventSeeds(f) {
		f.Add(seed)
	}
	f.Add([]byte(nil))
	f.Add([]byte{0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		ev, err := decodeEvent(data)
		if err != nil {
			return // invalid input is a valid outcome; only a panic is a bug
		}
		reencoded := encodeEvent(ev)
		ev2, err2 := decodeEvent(reencoded)
		if err2 != nil {
			t.Fatalf("decodeEvent succeeded once then failed on its own re-encoding: %v", err2)
		}
		if ev.Seq != ev2.Seq || ev.Kind != ev2.Kind || ev.Source != ev2.Source || !ev.Timestamp.Equal(ev2.Timestamp) {
			t.Fatalf("decodeEvent not idempotent via re-encoding: %+v != %+v", ev, ev2)
		}
	})
}
