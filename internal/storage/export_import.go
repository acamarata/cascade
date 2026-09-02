package storage

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: Import — the write half of the export/import pair, plus its
//
//	ImportOpts/ImportReport/ImportVersionError wire and control types.
//	Split from export.go under R-14.117 (Art.10.3's 300-line cap; this
//	file's line-by-line parse-and-apply loop is the larger of the two
//	halves).
//
// Design decisions this ticket makes explicit (the task text leaves each
// of these to the implementor):
//
//  1. Conflict strategy default: ImportOpts's zero value is
//     ConflictStrategyError (ConflictStrategy's zero constant), matching
//     the task text's "default Error" without needing callers to set
//     anything — an unconfigured ImportOpts{} already refuses collisions
//     rather than silently overwriting.
//
//  2. Validation-before-write ordering, so a malformed file cannot leave a
//     domain half-imported: (a) domain argument membership in the closed
//     R-14.5 set — checked first, before the reader is touched at all;
//     (b) header line parses as valid JSON with Type == "header"; (c)
//     header.Domain matches the domain argument (cross-domain refusal,
//     see below); (d) header.SchemaVersion >= the target's on-disk stamp;
//     (e) opts.PreImport(ctx), if non-nil. Only after all five pass does
//     Import open the write transaction — every refusal path above
//     returns before a single sql.Tx exists, so "zero rows written" is
//     structural (there is nothing to roll back), not merely tested for.
//
//  3. Cross-domain safety (R-14.5 isolation, this ticket's own concern
//     text): an export file names the domain it was captured from in its
//     header (ExportHeader.Domain). Import refuses outright — never
//     remaps — whenever that recorded domain differs from the domain
//     argument the caller passed. Silent remap was considered and
//     rejected: it would let a caller import domain A's file into domain B
//     by mistake with no signal, which is exactly the cross-domain data
//     laundering B/S-02.T5's capability-grant boundary
//     (internal/storage/capability.go) exists to prevent one layer down —
//     an export/import surface must not reopen that door from a different
//     angle. A caller that genuinely wants to move data between domains
//     must do so explicitly and visibly at a higher layer, not through an
//     Import call that pretends the file's origin does not matter.
//
//  4. The reserved PluginStorage namespace (R-14.100,
//     ReservedPluginHostNamespace) is never a valid Import target: it is
//     not a member of the closed AllDomains set, so validDomain (reused
//     from capability.go, same check Grant/Check already enforce) rejects
//     it at decision (a) above with no special-case code needed here.
//
//  5. Atomicity: once the write transaction opens, every row applies
//     inside it; a parse/decode failure or a Skip/Overwrite/Error
//     decision never commits early. On any error the deferred Rollback
//     (never reached if Commit already ran) discards every write the
//     transaction made, so "corrupt record N of M" leaves rows
//     1..N-1 unwritten too — no partial import.
//
// SPORT: internal.storage.export.Import/ADDED,
//
//	internal.storage.export.ImportOpts/ADDED,
//	internal.storage.export.ImportVersionError/ADDED (P1-E02-W1-S03-T3).

// ConflictStrategy selects how Import handles a row whose key already
// exists in the target domain.
type ConflictStrategy int

const (
	// ConflictStrategyError refuses the import with a typed collision
	// error and writes zero rows (the zero value — ImportOpts{}'s
	// default).
	ConflictStrategyError ConflictStrategy = iota
	// ConflictStrategySkip leaves the existing row untouched and counts it
	// in ImportReport.RowsSkipped.
	ConflictStrategySkip
	// ConflictStrategyOverwrite replaces the existing row's value and
	// counts it in ImportReport.RowsOverwritten.
	ConflictStrategyOverwrite
)

// ImportOpts configures Import.
type ImportOpts struct {
	// ConflictStrategy selects Skip/Overwrite/Error behavior for a
	// colliding key. The zero value is ConflictStrategyError.
	ConflictStrategy ConflictStrategy
	// PreImport, when non-nil, is called once before Import opens its
	// write transaction; a non-nil return refuses the import before any
	// row is touched. A nil PreImport skips this step entirely — this is
	// an internal API (full_desc), and the backup epic (Epic S) owns
	// wiring a real pre-import snapshot into this hook; no snapshot
	// integration is claimed by this ticket (Art.1 — see REACHABILITY in
	// this ticket's journal).
	PreImport func(ctx context.Context) error
}

// ImportReport summarizes one Import call's outcome.
type ImportReport struct {
	// RowsImported is the count of rows newly inserted (no prior key).
	RowsImported int
	// RowsSkipped is the count of colliding rows left untouched under
	// ConflictStrategySkip.
	RowsSkipped int
	// RowsOverwritten is the count of colliding rows replaced under
	// ConflictStrategyOverwrite.
	RowsOverwritten int
}

// ImportVersionError is returned when an export's header.SchemaVersion is
// older than the target database's on-disk schema_version stamp. A
// concrete typed error (never a bare string) so a caller can
// errors.As(err, &ImportVersionError{}) to recover GotVersion/MinVersion
// programmatically, while cascade.KindOf(err) still recovers KindIntegrity
// through Unwrap — the same two-level shape health.go's HealthCheckError
// already establishes in this package.
type ImportVersionError struct {
	// GotVersion is the export file's header.SchemaVersion.
	GotVersion int
	// MinVersion is the target database's on-disk schema_version stamp —
	// the floor GotVersion must meet or exceed.
	MinVersion int
	// err carries the taxonomy Kind (KindIntegrity) for cascade.KindOf.
	err *cascade.Error
}

func newImportVersionError(got, minVersion int) *ImportVersionError {
	return &ImportVersionError{
		GotVersion: got,
		MinVersion: minVersion,
		err: cascade.Newf(cascade.KindIntegrity,
			"storage: import refused: export schema_version %d is older than target minimum %d", got, minVersion),
	}
}

// Error implements the error interface.
func (e *ImportVersionError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped taxonomy error so errors.As/cascade.KindOf
// traverse through ImportVersionError transparently.
func (e *ImportVersionError) Unwrap() error { return e.err }

// Import reads r as a line-delimited JSON export stream (Export's format)
// and applies its rows into domain. See this file's package doc for the
// full validate-before-write ordering, the cross-domain refusal design
// decision, and the atomicity guarantee. Signature is contract-fixed by
// this ticket's task text.
func Import(ctx context.Context, db *sql.DB, domain DomainID, r io.Reader, opts ImportOpts) (ImportReport, error) {
	if !validDomain(domain) {
		return ImportReport{}, cascade.Newf(cascade.KindInvalidInput,
			"storage: import refused: domain %q is not a member of the closed R-14.5 domain set", domain)
	}

	br := bufio.NewReaderSize(r, 64*1024) // no fixed max — see readImportLine
	header, err := readHeaderLine(br)
	if err != nil {
		return ImportReport{}, err
	}
	if err := checkCrossDomain(header, domain); err != nil {
		return ImportReport{}, err
	}

	targetVersion, err := readSchemaVersion(ctx, db)
	if err != nil {
		return ImportReport{}, err
	}
	if header.SchemaVersion < targetVersion {
		return ImportReport{}, newImportVersionError(header.SchemaVersion, targetVersion)
	}

	if opts.PreImport != nil {
		if err := opts.PreImport(ctx); err != nil {
			return ImportReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: import PreImport hook refused")
		}
	}

	return importRows(ctx, db, domain, br, opts.ConflictStrategy)
}

// checkCrossDomain refuses header.Domain values that do not name domain
// exactly (design decision 3 above): never a remap, always a refusal.
func checkCrossDomain(header ExportHeader, domain DomainID) error {
	if header.Domain != string(domain) {
		return cascade.Newf(cascade.KindInvalidInput,
			"storage: import refused: export was captured from domain %q, refusing to import into domain %q (no cross-domain remap)",
			header.Domain, domain)
	}
	return nil
}

// readHeaderLine reads and validates the export stream's mandatory first
// line.
func readHeaderLine(br *bufio.Reader) (ExportHeader, error) {
	line, err := readImportLine(br)
	if err != nil {
		if err == io.EOF {
			return ExportHeader{}, cascade.New(cascade.KindIntegrity, "storage: import refused: empty export stream (no header line)")
		}
		return ExportHeader{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: import read header line")
	}
	var header ExportHeader
	if err := json.Unmarshal(line, &header); err != nil {
		return ExportHeader{}, cascade.Wrap(cascade.KindIntegrity, err, "storage: import parse header line")
	}
	if header.Type != recordTypeHeader {
		return ExportHeader{}, cascade.Newf(cascade.KindIntegrity,
			"storage: import refused: first line has _type %q, want %q", header.Type, recordTypeHeader)
	}
	return header, nil
}

// readImportLine reads one "\n"-terminated line via bufio.Reader.ReadBytes
// rather than bufio.Scanner: Scanner's default token cap
// (bufio.MaxScanTokenSize, 64KiB) would silently break on the "very large
// values" round-trip case this ticket's contract explicitly calls out —
// ReadBytes has no such cap and grows its result with the line, however
// large a single base64-encoded value line gets.
func readImportLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	line = bytes.TrimSuffix(line, []byte("\n"))
	if err == io.EOF {
		if len(line) == 0 {
			return nil, io.EOF
		}
		return line, nil // final line with no trailing newline
	}
	if err != nil {
		return nil, err
	}
	return line, nil
}

// importRows opens the single write transaction and applies every
// remaining line in br, per strategy. Any parse/decode/collision error
// returns without committing, so the deferred Rollback discards the whole
// transaction (design decision 5).
func importRows(ctx context.Context, db *sql.DB, domain DomainID, br *bufio.Reader, strategy ConflictStrategy) (ImportReport, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ImportReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: import begin write transaction")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := ensureKVTable(ctx, tx); err != nil {
		return ImportReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: import ensure kv table")
	}

	var report ImportReport
	for {
		line, err := readImportLine(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			return ImportReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: import read row line")
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue // tolerate a trailing blank line, never a data line
		}
		if err := applyImportLine(ctx, tx, domain, line, strategy, &report); err != nil {
			return ImportReport{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return ImportReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: import commit write transaction")
	}
	committed = true
	return report, nil
}
