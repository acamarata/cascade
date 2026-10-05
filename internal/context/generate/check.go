package generate

// Purpose: the drift check behind `cascade context generate --check`.
// Inputs: a repository root.
// Outputs: a Drift listing, per generated file, at most one of exactly
//   four red conditions: manifest-absent, file-missing, mangled-marker and
//   managed-block-edited.
// Constraints: read-only; it creates and writes nothing. Bytes outside the
//   markers never count, so a hand edit beside a managed block stays green.
//   A manifest that cannot be read or a path that escapes the root is an
//   error, not a drift item.
// SPORT: context-engine/generation-check (ADD, P1-GEN-05).

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The four drift conditions, in the exact words the report uses.
const (
	condManifestAbsent = "manifest-absent"
	condFileMissing    = "file-missing"
	condMangledMarker  = "mangled-marker"
	condBlockEdited    = "managed-block-edited"
)

// DriftItem is one red finding.
type DriftItem struct{ Path, Condition string }

// Drift is the result of Check.
type Drift struct{ Items []DriftItem }

// Clean reports whether nothing drifted.
func (d Drift) Clean() bool { return len(d.Items) == 0 }

// Check compares every manifest entry with the file on disk.
func Check(ctx context.Context, repoRoot string) (Drift, error) {
	m, ok, err := ReadManifest(repoRoot)
	if err != nil {
		return Drift{}, err
	}
	if !ok {
		return Drift{Items: []DriftItem{{Path: ManifestRel, Condition: condManifestAbsent}}}, nil
	}
	var d Drift
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return Drift{}, cascade.Wrap(cascade.KindCanceled, err, "generate: check canceled")
		}
		cond, err := checkEntry(repoRoot, e)
		if err != nil {
			return Drift{}, err
		}
		if cond != "" {
			d.Items = append(d.Items, DriftItem{Path: e.Path, Condition: cond})
		}
	}
	sort.Slice(d.Items, func(i, j int) bool { return d.Items[i].Path < d.Items[j].Path })
	return d, nil
}

// checkEntry returns the one condition entry e is in, or "" when it is
// green.
func checkEntry(repoRoot string, e Entry) (string, error) {
	form, err := FormFor(e.Path)
	if err != nil {
		return "", err
	}
	c, err := openConfined(repoRoot, e.Path)
	if err != nil {
		return "", err
	}
	defer c.close()
	data, ok, err := c.read()
	if err != nil {
		return "", err
	}
	if !ok {
		return condFileMissing, nil
	}
	blocks, err := ParseManagedBlocks(data, form, e.knownIDs())
	if err != nil {
		if isMangled(err) {
			return condMangledMarker, nil
		}
		return "", err
	}
	if blocksEdited(blocks, e) {
		return condBlockEdited, nil
	}
	return "", nil
}
