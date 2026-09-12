// Package secrets uses this file for all-or-rollback vault.env imports.
// Purpose: apply a fully parsed v1 vault batch without partial custody writes.
// Inputs: parsed vault.env bytes, a broker, and a dry-run flag.
// Outputs: created/updated/unchanged counts plus duplicate key names only.
// Constraints: values stay in memory; no temp file is created; a failed Set
// restores every earlier key before the typed failure is returned.
// SPORT: secrets/vault-import/CHANGED (P1-E26-W10-S53-T1).
package secrets

import (
	"bytes"
	"context"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// AtomicImportReport summarizes one atomic import without exposing values.
type AtomicImportReport struct {
	Parsed         int
	Created        int
	Updated        int
	Unchanged      int
	Names          []string
	DuplicateNames []string
	CreatedNames   []string
	UpdatedNames   []string
	UnchangedNames []string
}

type atomicImportEntry struct {
	name     string
	value    []byte
	existed  bool
	previous []byte
	changed  bool
}

// ImportAtomic parses the entire file, snapshots only the target values in
// memory, and then applies the changed keys. Any failure rolls back successful
// earlier writes. dryRun performs parsing and comparison only.
func ImportAtomic(ctx context.Context, b *Broker, data []byte, dryRun bool) (AtomicImportReport, error) {
	if b == nil {
		return AtomicImportReport{}, cascade.New(cascade.KindInvalidInput, "secrets: atomic import needs a broker")
	}
	parsed, err := ParseVaultEnv(data)
	if err != nil {
		return AtomicImportReport{}, err
	}
	report, plan, err := planAtomicImport(ctx, b, parsed)
	if err != nil {
		return AtomicImportReport{}, err
	}
	if dryRun {
		return report, nil
	}
	if err := applyAtomicImport(ctx, b, plan); err != nil {
		return AtomicImportReport{}, err
	}
	return report, nil
}

func planAtomicImport(ctx context.Context, b *Broker, parsed []EnvEntry) (AtomicImportReport, []atomicImportEntry, error) {
	latest, duplicates := collapseEnvEntries(parsed)
	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	names = sortedNames(names)
	existing, err := existingSecretNames(ctx, b)
	if err != nil {
		return AtomicImportReport{}, nil, err
	}
	report := AtomicImportReport{Parsed: len(parsed), Names: names, DuplicateNames: duplicates}
	plan := make([]atomicImportEntry, 0, len(names))
	for _, name := range names {
		entry, err := snapshotAtomicEntry(ctx, b, name, latest[name], existing[name])
		if err != nil {
			return AtomicImportReport{}, nil, err
		}
		countAtomicDecision(&report, entry)
		plan = append(plan, entry)
	}
	return report, plan, nil
}

func collapseEnvEntries(parsed []EnvEntry) (map[string][]byte, []string) {
	latest := make(map[string][]byte, len(parsed))
	duplicateSet := map[string]bool{}
	for _, entry := range parsed {
		if _, exists := latest[entry.Name]; exists {
			duplicateSet[entry.Name] = true
		}
		latest[entry.Name] = append([]byte(nil), entry.Value...)
	}
	duplicates := make([]string, 0, len(duplicateSet))
	for name := range duplicateSet {
		duplicates = append(duplicates, name)
	}
	return latest, sortedNames(duplicates)
}

func existingSecretNames(ctx context.Context, b *Broker) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "secrets: atomic import canceled")
	}
	names, err := b.custody.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out, nil
}

func snapshotAtomicEntry(ctx context.Context, b *Broker, name string, value []byte, existed bool) (atomicImportEntry, error) {
	entry := atomicImportEntry{name: name, value: value, existed: existed, changed: true}
	if !existed {
		return entry, nil
	}
	previous, err := b.custody.Get(ctx, name)
	if err != nil {
		return atomicImportEntry{}, err
	}
	entry.previous = append([]byte(nil), previous...)
	entry.changed = !bytes.Equal(previous, value)
	return entry, nil
}

func countAtomicDecision(report *AtomicImportReport, entry atomicImportEntry) {
	switch {
	case !entry.changed:
		report.Unchanged++
		report.UnchangedNames = append(report.UnchangedNames, entry.name)
	case entry.existed:
		report.Updated++
		report.UpdatedNames = append(report.UpdatedNames, entry.name)
	default:
		report.Created++
		report.CreatedNames = append(report.CreatedNames, entry.name)
	}
}

func applyAtomicImport(ctx context.Context, b *Broker, plan []atomicImportEntry) error {
	applied := make([]atomicImportEntry, 0, len(plan))
	for _, entry := range plan {
		if !entry.changed {
			continue
		}
		if err := ctx.Err(); err != nil {
			cause := cascade.Wrap(cascade.KindCanceled, err, "secrets: atomic import canceled")
			return rollbackAtomicImport(ctx, b, applied, cause)
		}
		if err := b.custody.Set(ctx, entry.name, entry.value); err != nil {
			return rollbackAtomicImport(ctx, b, append(applied, entry), err)
		}
		applied = append(applied, entry)
	}
	return nil
}

func rollbackAtomicImport(ctx context.Context, b *Broker, applied []atomicImportEntry, cause error) error {
	rollbackCtx := context.WithoutCancel(ctx)
	for i := len(applied) - 1; i >= 0; i-- {
		entry := applied[i]
		var err error
		if entry.existed {
			err = b.custody.Set(rollbackCtx, entry.name, entry.previous)
		} else {
			err = b.custody.Delete(rollbackCtx, entry.name)
		}
		if err != nil && (!errors.Is(err, cascade.ErrNotFound) || entry.existed) {
			return cascade.Wrap(cascade.KindIntegrity, err,
				"secrets: atomic import rollback failed; destination consistency is unknown")
		}
	}
	return cause
}
