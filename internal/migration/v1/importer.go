// Package v1 imports data written by the archived v1 runtime into v2's
// established stores. It contains no v1 implementation code. The parsers are
// derived from archived format evidence and are verified against harvested,
// provenance-documented v1 data under testdata/v1-goldens.
//
// Purpose: the single importer boundary and its deterministic result model.
// Inputs: a v1 home directory and a dry-run choice.
// Outputs: an exact change journal or a typed, fail-closed refusal.
// Constraints: parsing finishes before writes; importers are idempotent; no
// partial or guessed record is returned after malformed or unknown input.
// SPORT: migration/v1/ADD (P1-E26-W10-S53-T1).
package v1

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Domain identifies one independently imported v1 data family.
type Domain string

// The four supported v1 data families.
const (
	DomainMemory   Domain = "memory"
	DomainVault    Domain = "vault"
	DomainAccounts Domain = "accounts"
	DomainConfig   Domain = "config"
)

// Operation names one destination effect in a result.
type Operation string

// Supported destination effects. Unchanged is reported but is not a delta.
const (
	OperationCreate    Operation = "create"
	OperationUpdate    Operation = "update"
	OperationTombstone Operation = "tombstone"
	OperationSkip      Operation = "skip-existing"
	OperationUnchanged Operation = "unchanged"
)

// Request is the sole input accepted by every Importer.
type Request struct {
	// SourceRoot is a v1 home directory containing .cascade and, for older
	// vault installs, .claude.
	SourceRoot string
	// DryRun computes the exact result without mutating any destination.
	DryRun bool
}

// Change is one deterministic import decision. Source and Target are logical,
// relative names so journals never expose a machine-specific absolute path.
type Change struct {
	Operation   Operation `json:"operation"`
	Source      string    `json:"source"`
	Target      string    `json:"target"`
	ContentHash string    `json:"content_hash,omitempty"`
}

// JournalEntry records a non-secret fact the orchestrator must persist.
type JournalEntry struct {
	Code   string `json:"code"`
	Source string `json:"source,omitempty"`
	Detail string `json:"detail"`
}

// ReauthPrompt names one imported account that still needs authentication.
// No credential value can enter this type.
type ReauthPrompt struct {
	Account string   `json:"account"`
	Driver  string   `json:"driver"`
	Methods []string `json:"methods"`
}

// DryRunResult is returned for dry and live runs alike. Changes describes the
// same plan in both cases; Applied distinguishes observation from mutation.
type DryRunResult struct {
	Domain  Domain         `json:"domain"`
	Applied bool           `json:"applied"`
	Changes []Change       `json:"changes"`
	Journal []JournalEntry `json:"journal"`
	Reauth  []ReauthPrompt `json:"reauth"`
}

// Importer is the only callable importer surface. Concrete importer types and
// their store handles remain private to this package.
type Importer interface {
	Import(ctx context.Context, req Request) (DryRunResult, error)
}

// Domain sentinel errors. Each uses one member of pkg/cascade's frozen
// taxonomy; these are not new error kinds.
var (
	ErrUnknownInput    = cascade.New(cascade.KindInvalidInput, "migration v1: unknown input")
	ErrMalformedInput  = cascade.New(cascade.KindIntegrity, "migration v1: malformed input")
	ErrVersionMismatch = cascade.New(cascade.KindUnsupported, "migration v1: unsupported format version")
	ErrImportConflict  = cascade.New(cascade.KindConflict, "migration v1: destination conflict")
)
