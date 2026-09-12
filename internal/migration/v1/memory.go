// Package v1 uses this file for flat-memory import and v2 FileStore batch wiring.
// Purpose: connect parsed v1 memory data to the atomic memory batch adapter.
// Inputs: <v1-home>/.cascade/memory markdown files and tombstone markers.
// Outputs: v2 memory records with source path, mtime, kind and body preserved.
// Constraints: the whole source directory parses before ImportBatch writes;
// unknown files fail closed and batch failure restores the destination.
// SPORT: migration/v1/memory/ADD (P1-E26-W10-S53-T1).
package v1

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

type memoryImporter struct{ store *memory.FileStore }

// NewMemoryImporter returns the v1 memory importer through the sole Importer
// interface. The destination handle is retained privately.
func NewMemoryImporter(store *memory.FileStore) Importer {
	return &memoryImporter{store: store}
}

func (m *memoryImporter) Import(ctx context.Context, req Request) (DryRunResult, error) {
	result := DryRunResult{Domain: DomainMemory}
	if m.store == nil {
		return result.Normalize(), cascade.New(cascade.KindInvalidInput, "migration v1 memory: destination store is required")
	}
	mutations, err := readMemorySource(req.SourceRoot)
	if err != nil {
		return result.Normalize(), err
	}
	entries, err := m.store.ImportBatch(ctx, mutations, req.DryRun)
	if err != nil {
		return result.Normalize(), err
	}
	result.Applied = !req.DryRun
	for _, entry := range entries {
		result.Changes = append(result.Changes, memoryChange(entry))
	}
	result.Journal = append(result.Journal, JournalEntry{
		Code: "memory.provenance", Source: ".cascade/memory",
		Detail: "source paths, original mtimes, memory kinds, and exact source bytes preserved",
	})
	return result.Normalize(), nil
}

func memoryChange(entry memory.ImportBatchEntry) Change {
	operation := OperationCreate
	switch entry.Action {
	case memory.ImportTombstone:
		operation = OperationTombstone
	case memory.ImportUnchanged:
		operation = OperationUnchanged
	case memory.ImportCreate:
		operation = OperationCreate
	}
	return Change{
		Operation:   operation,
		Source:      filepath.ToSlash(strings.TrimPrefix(entry.SourceRef, "v1:")),
		Target:      entry.Kind.String() + "/" + entry.Name,
		ContentHash: entry.BodyHash,
	}
}
