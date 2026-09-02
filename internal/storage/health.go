package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// Purpose: StorageHealthCheck — the pluggable, genuinely-probing health
//
//	function C/S-05.T2's doctor framework calls for `cascade doctor
//	--storage`. This ticket ships the function only, not the CLI wiring.
//
// Inputs: an open *sql.DB against a cascade.db-shaped database (ideally
//
//	one Bootstrap has already run against, though every check degrades
//	informatively rather than panicking on an un-bootstrapped database).
//
// Outputs: a HealthReport with one CheckResult per probe; every failing
//
//	Result.Err is a *HealthCheckError wrapping a *cascade.Error, so a
//	caller can recover both the specific check name and the taxonomy
//	Kind via errors.As/cascade.KindOf.
//
// Constraints: Art.1 — every condition below is genuinely probed against
//
//	the real database handle; there is no hard-coded Result, and no
//	check ever short-circuits to OK without having executed its query.
//	See each checkXxx function's doc comment for exactly what it proves
//	and what it structurally cannot detect.
//
// SPORT: internal.storage.health.StorageHealthCheck/ADDED
//
//	(P1-E02-W1-S03-T1).

// minimumReaderVersion is the oldest on-disk schema_version this binary
// can still open. There are no migrations beyond the bootstrapSchemaVersion
// stamp yet, so the floor and the stamp are the same value; a later
// migration ticket that raises bootstrapSchemaVersion's successor versions
// will not need to touch this constant, since (b) only asserts "on-disk >=
// floor", not equality.
const minimumReaderVersion = bootstrapSchemaVersion

// HealthCheckError is the typed error every failing HealthReport
// CheckResult.Err carries. Check names the failing probe ("wal-mode",
// "schema-version", "domain-tables", "probe-write", "flock-probe") and Err
// is the taxonomy error describing why — Unwrap exposes Err so
// errors.As/cascade.KindOf still recover the Kind through this wrapper,
// satisfying "errors carry taxonomy kinds at any boundary a caller sees"
// without losing which specific check failed.
type HealthCheckError struct {
	Check string
	Err   *cascade.Error
}

func (e *HealthCheckError) Error() string {
	return fmt.Sprintf("storage health[%s]: %v", e.Check, e.Err)
}

// Unwrap exposes Err so errors.As(err, &cascade.Error{}) and
// cascade.KindOf(err) traverse through HealthCheckError transparently.
func (e *HealthCheckError) Unwrap() error { return e.Err }

// CheckResult is one probe's outcome within a HealthReport.
type CheckResult struct {
	OK     bool
	Detail string
	Err    error
}

// HealthReport is StorageHealthCheck's full result: one CheckResult per
// probe, named per the ticket's (a)-(e) list. A caller that wants "is
// everything fine" checks every field's OK, or ranges the Results() slice.
type HealthReport struct {
	WALMode       CheckResult
	SchemaVersion CheckResult
	DomainTables  CheckResult
	ProbeWrite    CheckResult
	FlockProbe    CheckResult
}

// Results returns the report's five checks as a name -> CheckResult map,
// for callers (the doctor framework, tests) that want to iterate rather
// than name each field.
func (r HealthReport) Results() map[string]CheckResult {
	return map[string]CheckResult{
		"wal-mode":       r.WALMode,
		"schema-version": r.SchemaVersion,
		"domain-tables":  r.DomainTables,
		"probe-write":    r.ProbeWrite,
		"flock-probe":    r.FlockProbe,
	}
}

// OK reports whether every check in the report passed.
func (r HealthReport) OK() bool {
	for _, res := range r.Results() {
		if !res.OK {
			return false
		}
	}
	return true
}

// StorageHealthCheck genuinely probes five conditions against db and
// returns a HealthReport describing each. No check is hard-coded: every
// branch below executes a real query or a real flock(2) attempt before
// deciding OK.
//
// Ticket P1-E02-W1-S03-T1's contract names this exact exported symbol
// verbatim ("exported as StorageHealthCheck(ctx, db *sql.DB) HealthReport
// in internal/storage/health.go") for C/S-05.T2's doctor framework to
// call; renaming to HealthCheck to silence revive's package-name-stutter
// hint would break that contract, hence the directive below.
//
//nolint:revive // contract-mandated exported name — see doc comment above
func StorageHealthCheck(ctx context.Context, db *sql.DB) HealthReport {
	return HealthReport{
		WALMode:       checkWALMode(ctx, db),
		SchemaVersion: checkSchemaVersion(ctx, db),
		DomainTables:  checkDomainTables(ctx, db),
		ProbeWrite:    checkProbeWrite(ctx, db),
		FlockProbe:    checkFlockProbe(ctx, db),
	}
}

// checkWALMode asserts `PRAGMA journal_mode` reports "wal". Detects: WAL
// not active (a database opened without the WAL DSN pragma, or one
// explicitly switched to DELETE/TRUNCATE/etc journal mode). Does NOT
// detect: whether WAL checkpointing is healthy, or whether the -wal/-shm
// sidecar files are present/writable on disk — only the connection's own
// reported mode.
func checkWALMode(ctx context.Context, db *sql.DB) CheckResult {
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode;`).Scan(&mode); err != nil {
		return failResult("wal-mode", cascade.Wrap(cascade.KindUnavailable, err, "storage: read journal_mode"))
	}
	if !strings.EqualFold(mode, "wal") {
		return failResult("wal-mode", cascade.Newf(cascade.KindIntegrity, "storage: journal_mode is %q, want wal", mode))
	}
	return CheckResult{OK: true, Detail: "journal_mode=wal"}
}

// checkSchemaVersion reads the on-disk applied_migrations stamp and
// asserts it is >= minimumReaderVersion. Detects: a missing/un-bootstrapped
// ledger table, or an on-disk version below the floor (this binary is too
// old to safely read the database). Does NOT detect: a schema newer than
// this binary understands beyond the floor comparison direction implied —
// that downgrade-refusal case belongs to internal/storage/migrate.Apply,
// not this read-only check (per the ticket's "read-only table access, no
// ledger-API dependency" constraint).
func checkSchemaVersion(ctx context.Context, db *sql.DB) CheckResult {
	exists, err := tableExists(ctx, db, bootstrapLedgerTable)
	if err != nil {
		return failResult("schema-version", cascade.Wrap(cascade.KindUnavailable, err, "storage: check applied_migrations presence"))
	}
	if !exists {
		return failResult("schema-version", cascade.New(cascade.KindIntegrity, "storage: applied_migrations table missing — database was never bootstrapped"))
	}

	var version sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT MAX(schema_version) FROM `+quoteIdent(bootstrapLedgerTable)).Scan(&version)
	if err != nil {
		return failResult("schema-version", cascade.Wrap(cascade.KindUnavailable, err, "storage: read schema_version stamp"))
	}
	if !version.Valid || version.Int64 < minimumReaderVersion {
		return failResult("schema-version", cascade.Newf(cascade.KindIntegrity, "storage: on-disk schema_version %d below minimum_reader_version %d", version.Int64, minimumReaderVersion))
	}
	return CheckResult{OK: true, Detail: fmt.Sprintf("schema_version=%d (floor %d)", version.Int64, minimumReaderVersion)}
}

// checkDomainTables queries sqlite_master for every AllDomains anchor
// table. Detects: any domain table dropped or never created. Does NOT
// detect: a domain table present but with the wrong SHAPE (this ticket's
// anchor tables have no schema beyond `id` to check yet — later tickets
// that add real per-domain schema also own asserting its shape).
func checkDomainTables(ctx context.Context, db *sql.DB) CheckResult {
	var missing []string
	for _, meta := range AllDomains {
		table := domainRootTable(meta.TablePrefix)
		exists, err := tableExists(ctx, db, table)
		if err != nil {
			return failResult("domain-tables", cascade.Wrapf(cascade.KindUnavailable, err, "storage: check domain table %s", table))
		}
		if !exists {
			missing = append(missing, string(meta.ID))
		}
	}
	if len(missing) > 0 {
		return failResult("domain-tables", cascade.Newf(cascade.KindIntegrity, "storage: missing domain anchor table(s): %v", missing))
	}
	return CheckResult{OK: true, Detail: fmt.Sprintf("%d/%d domain anchor tables present", len(AllDomains), len(AllDomains))}
}

// failResult builds a failing CheckResult from a *cascade.Error, wrapping
// it in a *HealthCheckError tagged with check so callers can recover both
// the specific probe name and the taxonomy Kind.
func failResult(check string, err *cascade.Error) CheckResult {
	return CheckResult{OK: false, Detail: err.Error(), Err: &HealthCheckError{Check: check, Err: err}}
}

// mainDBFilePath resolves the "main" database's on-disk file path via
// `PRAGMA database_list`, the only way to recover a path from a bare
// *sql.DB handle (StorageHealthCheck's signature carries no path
// parameter — see the package doc). An empty return means an in-memory
// database (":memory:" or no file backing), which the flock probe skips
// rather than treats as an error.
func mainDBFilePath(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA database_list;`)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", err
		}
		if name == "main" {
			return file, nil
		}
	}
	return "", rows.Err()
}

// checkFlockProbe resolves db's backing file and performs a non-blocking,
// non-acquiring §D-3 lock probe via providers/sqlite.ProbeExclusiveLock.
// Detects: another process (live daemon, or a lock left behind by one
// that crashed) currently holding the exclusive lock. Does NOT detect:
// which process holds it, or whether a held lock is "stale" versus live —
// see ProbeExclusiveLock's own doc comment for why that distinction is
// structurally unavailable at the flock(2) layer. On windows (tier-2
// scope), this check reports OK with the refusal recorded in Detail —
// per the ticket, an unsupported platform must not fail the health check.
func checkFlockProbe(ctx context.Context, db *sql.DB) CheckResult {
	path, err := mainDBFilePath(ctx, db)
	if err != nil {
		return failResult("flock-probe", cascade.Wrap(cascade.KindUnavailable, err, "storage: resolve main database file path"))
	}
	if path == "" {
		return CheckResult{OK: true, Detail: "in-memory database, flock probe skipped"}
	}

	result, err := sqlite.ProbeExclusiveLock(path)
	if err != nil {
		kind, ok := cascade.KindOf(err)
		if !ok {
			kind = cascade.KindUnavailable
		}
		return failResult("flock-probe", cascade.Wrap(kind, err, "storage: flock probe"))
	}
	switch {
	case result.Unsupported:
		return CheckResult{OK: true, Detail: "windows tier-2: " + result.Detail}
	case result.Held:
		return failResult("flock-probe", cascade.Newf(cascade.KindConflict, "storage: exclusive lock held by another process (%s)", result.Detail))
	default:
		return CheckResult{OK: true, Detail: "no exclusive lock held"}
	}
}
