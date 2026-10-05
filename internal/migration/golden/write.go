// Purpose: publish guarded fixtures and README provenance into the four
// owning migration/ dirs, idempotently.
// Inputs: the module root, the guarded fixtures and the injected clock.
// Outputs: written/unchanged counts; files only through writeFile.
// Constraints: a fixture whose identical bytes already exist is not
// rewritten (zero writes on a repeat run); a same-named file with different
// bytes, or any file in a migration/ dir that is neither README.md nor a
// fixture name, refuses. Fixtures are written before READMEs, so a failed
// write adds no provenance row. Every write is runtime.WriteFileAtomic.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// readmeName is the provenance file in each migration/ dir.
const readmeName = "README.md"

// fixtureNamePattern is the only fixture file name shape a migration/ dir
// may hold beside README.md.
var fixtureNamePattern = regexp.MustCompile(`^(memory|vault|accounts|config|config-refusal)-[0-9a-f]{16}\.json$`)

// writeFile publishes one file. A package var only so tests can inject a
// write failure; Run always uses runtime.WriteFileAtomic.
var writeFile = func(path string, data []byte) error {
	return runtime.WriteFileAtomic(path, data, 0o644)
}

// applyStats counts what a write pass did.
type applyStats struct {
	written, unchanged, readmes int
}

// readmePlan is one README to publish after the fixtures.
type readmePlan struct {
	path string
	data []byte
}

// applyFixtures renders and guards every README first, then writes every
// new fixture, then the READMEs whose bytes changed.
func applyFixtures(moduleRoot string, fixtures []fixture, clock runtime.Clock) (applyStats, error) {
	var stats applyStats
	sorted := append([]fixture(nil), fixtures...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	readmes, err := planReadmes(moduleRoot, sorted, clock.Now().UTC().Format("2006-01-02"))
	if err != nil {
		return stats, err
	}
	for _, fx := range sorted {
		wrote, err := writeFixture(filepath.Join(moduleRoot, filepath.FromSlash(outputDirs[fx.Domain])), fx)
		if err != nil {
			return stats, err
		}
		if wrote {
			stats.written++
		} else {
			stats.unchanged++
		}
	}
	for _, plan := range readmes {
		if err := writeFile(plan.path, plan.data); err != nil {
			return stats, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: write a README")
		}
		stats.readmes++
	}
	return stats, nil
}

// planReadmes checks every touched output dir and renders the README each
// one needs, returning only those whose bytes change.
func planReadmes(moduleRoot string, fixtures []fixture, captured string) ([]readmePlan, error) {
	rows := map[v1.Domain][]provenanceRow{}
	for _, fx := range fixtures {
		rows[fx.Domain] = append(rows[fx.Domain], fx.Row)
	}
	var out []readmePlan
	for _, domain := range inputDomains {
		if len(rows[domain]) == 0 {
			continue
		}
		if err := refuseLinkedPath(moduleRoot, outputDirs[domain]); err != nil {
			return nil, err
		}
		dir := filepath.Join(moduleRoot, filepath.FromSlash(outputDirs[domain]))
		if err := checkOutputDir(dir); err != nil {
			return nil, err
		}
		plan, changed, err := planReadme(dir, domain, rows[domain], captured)
		if err != nil {
			return nil, err
		}
		if changed {
			out = append(out, plan)
		}
	}
	return out, nil
}

// refuseLinkedPath refuses an output dir when any component of rel under
// moduleRoot is a symlink, so no write can land outside the root. The walk
// stops at the first component that does not exist yet (the dir is created
// later, as real directories). moduleRoot itself may be reached through a
// link; only the components below it are checked.
func refuseLinkedPath(moduleRoot, rel string) error {
	current := moduleRoot
	for _, part := range strings.Split(rel, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: inspect an output dir")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return cascade.Newf(cascade.KindIntegrity, "golden harvest: output path component %q is a symlink", part)
		}
	}
	return nil
}

// checkOutputDir refuses a migration/ dir holding anything but README.md
// and fixture-shaped regular files. A missing dir is fine.
func checkOutputDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: read an output dir")
	}
	for _, entry := range entries {
		ok := entry.Type().IsRegular() && (entry.Name() == readmeName || fixtureNamePattern.MatchString(entry.Name()))
		if !ok {
			return cascade.Newf(cascade.KindIntegrity, "golden harvest: unexpected file %q in an output dir", entry.Name())
		}
	}
	return nil
}

// writeFixture writes fx unless identical bytes are already there.
func writeFixture(dir string, fx fixture) (bool, error) {
	target := filepath.Join(dir, fx.Name)
	existing, err := os.ReadFile(target) //nolint:gosec // content-addressed name inside an output dir
	switch {
	case err == nil && bytes.Equal(existing, fx.Data):
		return false, nil
	case err == nil:
		return false, cascade.Newf(cascade.KindIntegrity, "golden harvest: %s exists with different bytes", fx.Name)
	case !os.IsNotExist(err):
		return false, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: read an existing fixture")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // committed testdata dir, world-readable by design
		return false, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: create an output dir")
	}
	if err := writeFile(target, fx.Data); err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "golden harvest: write %s", fx.Name)
	}
	return true, nil
}

// planReadme merges rows into dir's README provenance table and reports
// whether the rendered bytes differ from what is on disk.
func planReadme(dir string, domain v1.Domain, rows []provenanceRow, captured string) (readmePlan, bool, error) {
	target := filepath.Join(dir, readmeName)
	existing, err := os.ReadFile(target) //nolint:gosec // fixed README inside an output dir
	if err != nil && !os.IsNotExist(err) {
		return readmePlan{}, false, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: read a README")
	}
	merged, err := mergeRows(parseRows(string(existing)), rows, captured)
	if err != nil {
		return readmePlan{}, false, err
	}
	rendered := renderReadme(domain, merged)
	if bytes.Equal(existing, rendered) {
		return readmePlan{}, false, nil
	}
	if err := guardReadme(rendered); err != nil {
		return readmePlan{}, false, err
	}
	return readmePlan{path: target, data: rendered}, true, nil
}
