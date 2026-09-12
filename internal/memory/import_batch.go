// Package memory uses this file for the atomic v1 import batch boundary.
// Purpose: apply a complete translated memory batch or restore its snapshots.
// Inputs: fully parsed import mutations with source provenance and timestamps.
// Outputs: an exact plan, applied atomically or returned as a dry run.
// Constraints: preflight refuses conflicts before writes; any write failure
// restores every touched entry and tombstone to its original bytes.
// SPORT: memory/import-batch/ADD (P1-E26-W10-S53-T1).
package memory

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ImportMutation is one v1 memory record or tombstone translated to the v2
// model. SourceRef is persisted as the file-origin reference.
type ImportMutation struct {
	Entry      MemoryEntry
	Tombstone  bool
	SourceRef  string
	SourceTime time.Time
}

// ImportAction names the decision ImportBatch made for one mutation.
type ImportAction string

// Import batch decisions.
const (
	ImportCreate    ImportAction = "create"
	ImportTombstone ImportAction = "tombstone"
	ImportUnchanged ImportAction = "unchanged"
)

// ImportBatchEntry is one result row, in input order.
type ImportBatchEntry struct {
	Kind      MemoryKind
	Name      string
	SourceRef string
	BodyHash  string
	Action    ImportAction
}

// ImportBatch applies every mutation as one logical unit. dryRun executes the
// same preflight and returns the same plan without writing.
func (s *FileStore) ImportBatch(ctx context.Context, mutations []ImportMutation, dryRun bool) ([]ImportBatchEntry, error) {
	plan, err := s.planImport(ctx, mutations)
	if err != nil {
		return nil, err
	}
	result := make([]ImportBatchEntry, len(plan))
	for i := range plan {
		result[i] = plan[i].result
	}
	if dryRun || len(plan) == 0 {
		return result, nil
	}
	if err := s.applyImportPlan(ctx, plan); err != nil {
		return nil, err
	}
	return result, nil
}

type importPlanEntry struct {
	mutation ImportMutation
	result   ImportBatchEntry
	snapshot importSnapshot
	data     []byte
}

type importSnapshot struct {
	entryData       []byte
	entryExists     bool
	tombstoneData   []byte
	tombstoneExists bool
}

func (s *FileStore) planImport(ctx context.Context, mutations []ImportMutation) ([]importPlanEntry, error) {
	seen := make(map[string]bool, len(mutations))
	plan := make([]importPlanEntry, 0, len(mutations))
	for _, mutation := range mutations {
		if err := ctx.Err(); err != nil {
			return nil, cascade.Wrap(cascade.KindCanceled, err, "memory import canceled")
		}
		key := string(mutation.Entry.Kind) + "/" + mutation.Entry.Name
		if seen[key] {
			return nil, cascade.Newf(cascade.KindInvalidInput, "memory import repeats %q", key)
		}
		seen[key] = true
		entry, err := s.planOneImport(mutation)
		if err != nil {
			return nil, err
		}
		plan = append(plan, entry)
	}
	return plan, nil
}

func (s *FileStore) planOneImport(m ImportMutation) (importPlanEntry, error) {
	if err := validateImportMutation(m, s.clock.Now()); err != nil {
		return importPlanEntry{}, err
	}
	snapshot, err := s.snapshotImport(m.Entry.Kind, m.Entry.Name)
	if err != nil {
		return importPlanEntry{}, err
	}
	if m.Tombstone {
		return s.planTombstone(m, snapshot)
	}
	return s.planLiveImport(m, snapshot)
}

func validateImportMutation(m ImportMutation, now time.Time) error {
	if m.SourceRef == "" || m.SourceTime.IsZero() {
		return cascade.New(cascade.KindInvalidInput, "memory import requires source reference and timestamp")
	}
	if err := checkKey(m.Entry.Kind, m.Entry.Name); err != nil {
		return err
	}
	if m.Tombstone {
		return nil
	}
	return m.Entry.Validate(now)
}

func (s *FileStore) planLiveImport(m ImportMutation, snap importSnapshot) (importPlanEntry, error) {
	entry := canonicalImportedEntry(m)
	data := encodeEntry(entry)
	result := importResult(m, entry.BodyHash(), ImportCreate)
	if snap.tombstoneExists {
		return importPlanEntry{}, importConflict(entry, "a tombstone already exists")
	}
	if snap.entryExists {
		if bytes.Equal(snap.entryData, data) {
			result.Action = ImportUnchanged
			return importPlanEntry{mutation: m, result: result, snapshot: snap, data: data}, nil
		}
		return importPlanEntry{}, importConflict(entry, "different destination data already exists")
	}
	return importPlanEntry{mutation: m, result: result, snapshot: snap, data: data}, nil
}

func (s *FileStore) planTombstone(m ImportMutation, snap importSnapshot) (importPlanEntry, error) {
	result := importResult(m, "", ImportTombstone)
	if snap.tombstoneExists {
		result.Action = ImportUnchanged
		return importPlanEntry{mutation: m, result: result, snapshot: snap}, nil
	}
	if snap.entryExists {
		stored, err := decodeEntry(snap.entryData)
		if err != nil {
			return importPlanEntry{}, err
		}
		if stored.Provenance.Origin != OriginFile || stored.Provenance.SessionID != m.SourceRef {
			return importPlanEntry{}, importConflict(m.Entry, "destination record has different provenance")
		}
	}
	return importPlanEntry{mutation: m, result: result, snapshot: snap}, nil
}

func canonicalImportedEntry(m ImportMutation) MemoryEntry {
	entry := m.Entry.canonical()
	entry.Provenance.Origin = OriginFile
	entry.Provenance.SessionID = m.SourceRef
	entry.Provenance.CreatedAt = m.SourceTime.UTC()
	entry.Provenance.UpdatedAt = m.SourceTime.UTC()
	entry.Provenance.ContentHash = entry.BodyHash()
	return entry
}

func importResult(m ImportMutation, hash string, action ImportAction) ImportBatchEntry {
	return ImportBatchEntry{
		Kind: m.Entry.Kind, Name: m.Entry.Name, SourceRef: m.SourceRef,
		BodyHash: hash, Action: action,
	}
}

func importConflict(entry MemoryEntry, reason string) error {
	return cascade.Newf(cascade.KindConflict, "memory import %s/%s refused: %s", entry.Kind, entry.Name, reason)
}

func (s *FileStore) snapshotImport(kind MemoryKind, name string) (importSnapshot, error) {
	entry, entryExists, err := s.readImportFile(s.entryPath(kind, name))
	if err != nil {
		return importSnapshot{}, err
	}
	tombstone, tombstoneExists, err := s.readImportFile(s.tombstonePath(kind, name))
	if err != nil {
		return importSnapshot{}, err
	}
	return importSnapshot{
		entryData: entry, entryExists: entryExists,
		tombstoneData: tombstone, tombstoneExists: tombstoneExists,
	}, nil
}

func (s *FileStore) readImportFile(path string) ([]byte, bool, error) {
	exists, err := s.fs.Exists(path)
	if err != nil {
		return nil, false, cascade.Wrap(cascade.KindUnavailable, err, "memory import: inspect destination")
	}
	if !exists {
		return nil, false, nil
	}
	data, err := s.fs.ReadFile(path)
	if err != nil {
		return nil, false, cascade.Wrap(cascade.KindUnavailable, err, "memory import: read destination")
	}
	return data, true, nil
}

func (s *FileStore) applyImportPlan(ctx context.Context, plan []importPlanEntry) error {
	for i := range plan {
		if err := ctx.Err(); err != nil {
			return s.rollbackImport(plan[:i], cascade.Wrap(cascade.KindCanceled, err, "memory import canceled"))
		}
		if plan[i].result.Action == ImportUnchanged {
			continue
		}
		if err := s.applyImportEntry(plan[i]); err != nil {
			return s.rollbackImport(plan[:i+1], err)
		}
	}
	return nil
}

func (s *FileStore) applyImportEntry(entry importPlanEntry) error {
	kind, name := entry.mutation.Entry.Kind, entry.mutation.Entry.Name
	if entry.mutation.Tombstone {
		if err := s.fs.WriteAtomic(s.tombstonePath(kind, name), nil); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "memory import: write tombstone")
		}
		if err := s.removeImportFile(s.entryPath(kind, name)); err != nil {
			return err
		}
		return nil
	}
	if err := s.fs.WriteAtomic(s.entryPath(kind, name), entry.data); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "memory import: write record")
	}
	return nil
}

func (s *FileStore) rollbackImport(plan []importPlanEntry, cause error) error {
	for i := len(plan) - 1; i >= 0; i-- {
		if err := s.restoreImportSnapshot(plan[i]); err != nil {
			return cascade.Wrap(cascade.KindIntegrity, err, "memory import rollback failed after refusal")
		}
	}
	return cause
}

func (s *FileStore) restoreImportSnapshot(entry importPlanEntry) error {
	kind, name := entry.mutation.Entry.Kind, entry.mutation.Entry.Name
	if err := s.restoreImportFile(s.entryPath(kind, name), entry.snapshot.entryData, entry.snapshot.entryExists); err != nil {
		return err
	}
	return s.restoreImportFile(s.tombstonePath(kind, name), entry.snapshot.tombstoneData, entry.snapshot.tombstoneExists)
}

func (s *FileStore) restoreImportFile(path string, data []byte, existed bool) error {
	if existed {
		return s.fs.WriteAtomic(path, data)
	}
	return s.removeImportFile(path)
}

func (s *FileStore) removeImportFile(path string) error {
	if err := s.fs.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cascade.Wrap(cascade.KindUnavailable, err, "memory import: remove destination file")
	}
	return nil
}
