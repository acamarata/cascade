// Purpose: FuzzCronParse (task 9) — proves ParseSpec and Schedule.NextAfter
//   never panic for any input string, well-formed or adversarial. Seeds
//   from the hand-curated corpus at internal/testdata/fuzz/scheduler/
//   (06-FORGE-SPEC.md §5.7: fuzz corpora live under
//   internal/testdata/fuzz/, never beside the owning package — mirrors
//   internal/events/fuzz_test.go's loadFuzzEventSeeds pattern and
//   internal/runtime/fuzz_test.go's readFuzzSeedLines pattern).
// Constraints: no network calls (Art.7.2); reads only.
// SPORT: internal.events.scheduler.ParseSpec/ADDED (FuzzCronParse)
//   (P1-E03-W1-S04-T4).

package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fuzzCronCorpusDir is the shared fuzz-corpus home for FuzzCronParse,
// relative to this package's directory (internal/events/scheduler -> repo
// root is three levels up).
const fuzzCronCorpusDir = "../../../internal/testdata/fuzz/scheduler"

// readFuzzCronSeeds reads cron_seeds.txt's lines (mirrors
// internal/runtime/fuzz_test.go's readFuzzSeedLines exactly, duplicated
// rather than shared since neither package imports the other's _test.go
// helpers).
func readFuzzCronSeeds(f *testing.F) []string {
	f.Helper()
	data, err := os.ReadFile(filepath.Join(fuzzCronCorpusDir, "cron_seeds.txt"))
	if err != nil {
		f.Fatalf("reading fuzz corpus %s/cron_seeds.txt: %v", fuzzCronCorpusDir, err)
	}
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, string(data[start:i]))
			start = i + 1
		}
	}
	return lines
}

// FuzzCronParse proves ParseSpec never panics for any input string, and
// that anything it DOES accept can also have NextAfter computed on it
// without panicking (a bounded search — cron.go's nextSearchBound — so
// this also proves NextAfter always terminates rather than looping
// forever on an unsatisfiable field combination).
func FuzzCronParse(f *testing.F) {
	for _, seed := range readFuzzCronSeeds(f) {
		f.Add(seed)
	}
	f.Add("@every 1h")
	f.Add("* * * * *")
	f.Add("")
	f.Add("not a cron spec")
	f.Add("@every")
	f.Add("60 60 60 60 60")
	f.Add("*/0 * * * *")

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(_ *testing.T, spec string) {
		sched, err := ParseSpec(spec)
		if err != nil {
			return // invalid input is a valid outcome; only a panic is a bug
		}
		if _, err := sched.NextAfter(from); err != nil {
			return // "no occurrence within the search bound" is also valid
		}
	})
}
