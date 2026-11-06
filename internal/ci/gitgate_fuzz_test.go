// Purpose (this file): FuzzPreReceiveStdin drives ParsePreReceiveStdin with
// malformed and adversarial pre-receive input. Seed corpus at
// internal/ci/testdata/fuzz/FuzzPreReceiveStdin/.
// SPORT: internal.ci.ParsePreReceiveStdin/FUZZED (P1-CI-05a).
package ci

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FuzzPreReceiveStdin's property under test: never panic, and every input
// either parses to refs that each pass the ref rules, or returns a
// KindInvalidInput error with no refs.
func FuzzPreReceiveStdin(f *testing.F) {
	old, nw := strings.Repeat("a", 40), strings.Repeat("b", 40)
	f.Add(old + " " + nw + " refs/heads/main\n")
	f.Add(strings.Repeat("0", 40) + " " + nw + " refs/tags/v1\n")
	f.Add(old + " " + strings.Repeat("c", 64) + " refs/heads/main\n")
	f.Add(old + "  " + nw + " refs/heads/main\n")
	f.Add(old + " " + nw + " refs/heads/../bad\n\n")
	f.Add("")
	f.Add("oops")
	f.Fuzz(func(t *testing.T, in string) {
		refs, err := ParsePreReceiveStdin(strings.NewReader(in))
		if err != nil {
			if refs != nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("error %v with refs %v", err, refs)
			}
			return
		}
		seen := make(map[string]bool)
		for _, r := range refs {
			if validatePreReceiveRef(r) != nil || seen[r.Ref] {
				t.Fatalf("accepted bad ref %+v", r)
			}
			seen[r.Ref] = true
		}
	})
}
