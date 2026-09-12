// Package v1 uses this file to import vault.env through the established parser.
// Purpose: import a real v1 vault.env through the established parser and
// Broker custody boundary.
// Inputs: exactly one documented v1 vault path under the supplied home.
// Outputs: an idempotent, all-or-rollback vault delta and duplicate warnings.
// Constraints: values stay in memory, REDACTED fixtures refuse, and no temp
// file is created by this package or the atomic broker import.
// SPORT: migration/v1/vault/ADD (P1-E26-W10-S53-T1).
package v1

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

const maxVaultBytes = 16 << 20

type vaultImporter struct{ broker *secrets.Broker }

// NewVaultImporter returns the vault importer through the sole Importer
// interface. The broker and every credential value remain private.
func NewVaultImporter(broker *secrets.Broker) Importer {
	return &vaultImporter{broker: broker}
}

func (v *vaultImporter) Import(ctx context.Context, req Request) (DryRunResult, error) {
	result := DryRunResult{Domain: DomainVault}
	if v.broker == nil {
		return result.Normalize(), cascade.New(cascade.KindInvalidInput, "migration v1 vault: broker is required")
	}
	data, source, err := readVaultSource(req.SourceRoot)
	if err != nil {
		return result.Normalize(), err
	}
	if err := refuseRedactedVault(data); err != nil {
		return result.Normalize(), err
	}
	report, err := secrets.ImportAtomic(ctx, v.broker, data, req.DryRun)
	if err != nil {
		return result.Normalize(), err
	}
	result.Applied = !req.DryRun
	appendVaultChanges(&result, source, report)
	for _, name := range report.DuplicateNames {
		result.Journal = append(result.Journal, JournalEntry{
			Code: "vault.duplicate-last-wins", Source: source,
			Detail: "duplicate key " + name + " resolved to its last assignment",
		})
	}
	return result.Normalize(), nil
}

func readVaultSource(root string) ([]byte, string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, "", cascade.New(cascade.KindInvalidInput, "migration v1 vault: source root is required")
	}
	candidates := []string{filepath.Join(root, ".claude", "vault.env"), filepath.Join(root, ".cascade", "vault.env")}
	found := ""
	for _, path := range candidates {
		info, err := os.Lstat(path)
		switch {
		case err == nil && info.Mode().IsRegular():
			if found != "" {
				return nil, "", cascade.Wrap(cascade.KindConflict, ErrImportConflict,
					"migration v1 vault: both documented vault locations exist")
			}
			found = path
		case err == nil:
			return nil, "", cascade.Wrap(cascade.KindInvalidInput, ErrUnknownInput,
				"migration v1 vault: vault.env is not a regular file")
		case !os.IsNotExist(err):
			return nil, "", cascade.Wrap(cascade.KindUnavailable, err, "migration v1 vault: inspect source")
		}
	}
	if found == "" {
		return nil, "", cascade.New(cascade.KindNotFound, "migration v1 vault: no vault.env found")
	}
	return readBoundedVault(found, root)
}

func readBoundedVault(path, root string) ([]byte, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", cascade.Wrap(cascade.KindUnavailable, err, "migration v1 vault: stat source")
	}
	if info.Size() > maxVaultBytes {
		return nil, "", cascade.Wrap(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 vault: source exceeds the size limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", cascade.Wrap(cascade.KindUnavailable, err, "migration v1 vault: read source")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, "", cascade.Wrap(cascade.KindInternal, err, "migration v1 vault: derive source name")
	}
	return data, filepath.ToSlash(rel), nil
}

func refuseRedactedVault(data []byte) error {
	entries, err := secrets.ParseVaultEnv(data)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if bytes.Equal(entry.Value, []byte("REDACTED")) {
			return cascade.Wrapf(cascade.KindIntegrity, ErrMalformedInput,
				"migration v1 vault: key %q contains the REDACTED fixture sentinel", entry.Name)
		}
	}
	return nil
}

func appendVaultChanges(result *DryRunResult, source string, report secrets.AtomicImportReport) {
	for _, name := range report.CreatedNames {
		result.Changes = append(result.Changes, Change{Operation: OperationCreate, Source: source, Target: name})
	}
	for _, name := range report.UpdatedNames {
		result.Changes = append(result.Changes, Change{Operation: OperationUpdate, Source: source, Target: name})
	}
	for _, name := range report.UnchangedNames {
		result.Changes = append(result.Changes, Change{Operation: OperationUnchanged, Source: source, Target: name})
	}
}
