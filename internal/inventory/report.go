// Purpose: the counts surface's data shape. Report is the ONE struct both
//
//	the CLI view (`cascade doctor counts`) and the tracked JSON artifact
//	(counts.json) render, so a website reading the artifact and an operator
//	reading the CLI can never see a different shape for the same fact.
//
// Inputs: none — this file only declares the shape and its human rendering.
// Outputs: Report, PlatformList (a named type so Platforms carries a stable
//
//	String() rather than Go's default slice-of-string formatting).
//
// Constraints: every field here must be reproducible from a stated source
//
//	(Live and generator files document which); never a field a human typed
//	once and forgot.
//
// SPORT: internal.inventory.Report/ADDED.

package inventory

import "fmt"

// Report is the derived-counts snapshot: some fields are computed fresh at
// call time from a live artifact (ErrorKinds, StorageDomains, CLICommands
// — see live.go), the rest from the tracked tree at generation time
// (Providers, Plugins, SPORTLines, Platforms — see tree.go and counts.json)
// because an installed binary has no source tree to walk.
type Report struct {
	// GeneratedAt is the RFC3339 instant the report was assembled, via an
	// injected runtime.Clock — never a bare time.Now (Art.7.3).
	GeneratedAt string `json:"generated_at"`
	// ErrorKinds is len(pkg/cascade.AllKinds()): live, from the frozen
	// taxonomy itself.
	ErrorKinds int `json:"error_kinds"`
	// StorageDomains is len(internal/storage.AllDomains): live, from the
	// closed domain enumeration itself.
	StorageDomains int `json:"storage_domains"`
	// CLICommands is the count of the real cobra command tree the running
	// binary built: live, from the same tree the binary dispatches on.
	CLICommands int `json:"cli_commands"`
	// Providers is the count of top-level directories under providers/,
	// baked in at generation time (see tree.go's ComputeTreeCounts).
	Providers int `json:"providers"`
	// Plugins is the count of top-level directories under plugins/, baked
	// in at generation time.
	Plugins int `json:"plugins"`
	// SPORTLines is the count of "// SPORT:" comment lines across every
	// tracked .go file, baked in at generation time. It counts LINES, not
	// deduplicated entities: a multi-line SPORT comment continuation is
	// not counted twice only when it does not repeat the marker itself.
	// This is a raw signal, not a queryable registry — see doc.go's
	// limitations note.
	SPORTLines int `json:"sport_lines"`
	// Platforms is the deduplicated, sorted GOOS list the CI build+test
	// matrix in .github/workflows/ci.yml declares, baked in at generation
	// time (see tree.go's PlatformsFromCI). Cross-compile-only entries
	// (the darwin/amd64 build+vet lane) count once, same as a native one:
	// this field answers "how many operating systems", not "how many
	// matrix jobs".
	Platforms []string `json:"platforms"`
}

// PlatformCount returns len(r.Platforms), the derived scalar most CLI/JSON
// consumers actually want (a website tile, a doctor line) without forcing
// every caller to len() the slice itself.
func (r Report) PlatformCount() int { return len(r.Platforms) }

// String renders the human-mode table `cascade doctor counts` prints
// without --json. Kept to one line per fact, matching this tree's other
// doctor views (bundleView, statusHumanView).
func (r Report) String() string {
	return fmt.Sprintf(
		"counts (as of %s):\n"+
			"  error kinds:     %d\n"+
			"  storage domains: %d\n"+
			"  cli commands:    %d\n"+
			"  providers:       %d\n"+
			"  plugins:         %d\n"+
			"  sport lines:     %d\n"+
			"  platforms:       %d (%v)\n",
		r.GeneratedAt, r.ErrorKinds, r.StorageDomains, r.CLICommands,
		r.Providers, r.Plugins, r.SPORTLines, r.PlatformCount(), r.Platforms,
	)
}
