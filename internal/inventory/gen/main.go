// Purpose: regenerate internal/inventory/counts.json from the real,
//
//	checked-out tree. Run via `go run ./internal/inventory/gen` from
//	anywhere inside the repo (it resolves the repo root itself); never
//	hand-edit counts.json.
//
// Inputs: the checkout at the resolved repo root (providers/, plugins/,
//
//	every tracked .go file, .github/workflows/ci.yml).
//
// Outputs: internal/inventory/counts.json and
//
//	internal/inventory/sport/registry.json, both overwritten in place.
//
// Constraints: deterministic — running this twice in a row on an
//
//	unchanged tree must produce byte-identical output for both files
//	(asserted by internal/inventory's TestRenderCounts_Idempotent and
//	internal/inventory/sport's TestRenderRegistry_Idempotent, which invoke
//	the same computations this main wraps rather than shelling out to
//	itself). Never writes to os.Stdout/os.Stderr directly (internal/output's
//	contract, R-14.137, applies to every binary this module builds, not
//	only cmd/).
//
// SPORT: internal.inventory.gen/ADDED, internal.inventory.gen.registry/ADDED.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/acamarata/cascade/internal/inventory"
	"github.com/acamarata/cascade/internal/inventory/sport"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

func main() {
	w := output.NewDefault(false, false, false, false)
	if err := run(w); err != nil {
		w.Fail(err)
		os.Exit(1)
	}
}

func run(w *output.Writer) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	clock := runtime.NewSystemClock()
	if err := writeCounts(w, root, clock); err != nil {
		return err
	}
	return writeRegistry(w, root, clock)
}

// writeCounts regenerates internal/inventory/counts.json.
func writeCounts(w *output.Writer, root string, clock runtime.Clock) error {
	data, err := inventory.RenderCounts(root, clock)
	if err != nil {
		return err
	}
	outPath := root + "/internal/inventory/counts.json"
	if err := os.WriteFile(outPath, data, 0o644); err != nil { //nolint:gosec // generated artifact, not a secret
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	w.Println("wrote", outPath)
	return nil
}

// writeRegistry regenerates internal/inventory/sport/registry.json.
func writeRegistry(w *output.Writer, root string, clock runtime.Clock) error {
	data, err := sport.RenderRegistry(root, clock)
	if err != nil {
		return err
	}
	outPath := root + "/internal/inventory/sport/registry.json"
	if err := os.WriteFile(outPath, data, 0o644); err != nil { //nolint:gosec // generated artifact, not a secret
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	w.Println("wrote", outPath)
	return nil
}

// repoRoot resolves the checkout root via `git rev-parse --show-toplevel`,
// the same resolution every AGENT-BRIEF journal path in this repo uses.
func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
