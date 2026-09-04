package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: define the CLOSED cascade.db domain layout — R-14.5's ratified
//
//	list, amended to ELEVEN by R-16.51 — as a typed enum, plus Bootstrap,
//	the idempotent
//	function that stamps a fresh or existing cascade.db with each
//	domain's anchor table, WAL mode, and the initial schema_version.
//
// Inputs: Bootstrap takes an open *sql.DB and BootstrapOpts (a Clock,
//
//	required, plus an optional GrantRegistry seam for B/S-02.T5's
//	self-grant registration, not yet in-tree).
//
// Outputs: BootstrapReport{TablesCreated, Delta}, or a *cascade.Error.
// Constraints: the domain set is CLOSED by R-14.5 as amended by R-16.51 —
//
//	DomainID is a defined string type with exactly eleven constants
//	(the R-14.5 ten plus `policy`), never a bare string at any
//	call site. golangci-lint's exhaustive analyzer (R-14.101,
//	default-signifies-exhaustive: false) is enabled repo-wide, so any
//	future switch over DomainID that omits a case — including one added
//	by a later R-14.5 amendment — fails the lint wall rather than
//	silently compiling. This ticket does not import
//	internal/storage/migrate: the applied_migrations ledger table SHAPE
//	is shared by spec only (06-FORGE-SPEC.md / B/S-02.T3), never by code
//	import — Bootstrap issues its own CREATE TABLE IF NOT EXISTS against
//	that shape and stamps one row directly.
//
// SPORT: internal.storage.domains.AllDomains/ADDED,
//
//	internal.storage.domains.Bootstrap/ADDED (P1-E02-W1-S03-T1).

// DomainID identifies one of the eleven cascade.db domains RATIFIED as
// forged by T0 ruling R-14.5 and amended by R-16.51. The set is closed:
// adding, removing, or renaming a domain requires a T0 amendment to R-14.5
// (R-16.51 is exactly such an amendment, and the only one so far — it adds
// `policy`, the domain internal/policy's grants, standing grants, deny-list
// patterns, autonomy-profile state and classifier cache live in; the
// approval-token single-use ledger stays in `audit` per 06 §5.24). Exactly
// as pkg/cascade.Kind's
// taxonomy is closed by R-14.3). DomainID is a defined string type so a
// typo is a compile-time type mismatch, never a silently-wrong runtime
// string threaded through Store's namespace argument.
type DomainID string

// The closed eleven-domain enumeration: the R-14.5 ten ("context, memory,
// audit, secrets, sessions, config, retrieval, blobs, queue, jobs ... jobs
// and sessions ARE distinct domains") plus R-16.51's `policy`, appended
// last so the ten ratified positions keep the order every existing
// consumer already observes. Declaration order here is AllDomains's
// order, and AllDomains is built from this exact sequence (never map
// iteration), so both are deterministic across runs and across builds.
const (
	DomainContext   DomainID = "context"
	DomainMemory    DomainID = "memory"
	DomainAudit     DomainID = "audit"
	DomainSecrets   DomainID = "secrets"
	DomainSessions  DomainID = "sessions"
	DomainConfig    DomainID = "config"
	DomainRetrieval DomainID = "retrieval"
	DomainBlobs     DomainID = "blobs"
	DomainQueue     DomainID = "queue"
	DomainJobs      DomainID = "jobs"
	// DomainPolicy is the R-16.51 eleventh domain: internal/policy's
	// grants, standing grants, deny-list patterns, autonomy-profile state
	// and classifier cache. Registered by P1-E09-W2-S17-T1; the domain's
	// migration is authored by the B/S-02.T3 migration builder.
	DomainPolicy DomainID = "policy"
)

// ReservedPluginHostNamespace is the R-14.100 reserved PluginStorage
// namespace prefix ("plugin.__host__.metadata/<name>", host-owned,
// O/S-32.T3/T4). This ticket does not implement PluginStorage — that is
// O/S-32's surface, layered on pkg/provider.Store's namespace-scoped kv
// table, not on the domain-anchor tables Bootstrap creates here — but
// domains_test.go's isolation test asserts none of this ticket's
// TablePrefix values collides with it, so the closed domain set never
// encroaches on a namespace R-14.100 already reserved elsewhere. Exported
// so that test (and any future consumer) can assert against it directly
// rather than duplicating the literal string.
const ReservedPluginHostNamespace = "plugin.__host__"

// DomainMeta describes one cascade.db domain: its typed ID, the anchor-
// table-name prefix Bootstrap uses (and later tickets extend with the
// domain's real schema), and a human-readable pointer to the owning
// package/epic — documentation metadata only, not an import-boundary
// enforced by tooling.
type DomainMeta struct {
	ID          DomainID
	TablePrefix string
	OwnerPkg    string
}

// AllDomains is the closed, deterministically-ordered list of every
// cascade.db domain. Consumers that must handle every domain (Bootstrap,
// StorageHealthCheck, `cascade doctor --storage`, and any future
// domain-aware code) range over this slice rather than hard-coding the
// eleven values, so a future R-14.5
// amendment changes only this one declaration.
var AllDomains = []DomainMeta{
	{ID: DomainContext, TablePrefix: "context", OwnerPkg: "internal/context (Epic E)"},
	{ID: DomainMemory, TablePrefix: "memory", OwnerPkg: "internal/memory (Epic G)"},
	{ID: DomainAudit, TablePrefix: "audit", OwnerPkg: "internal/audit (Epic I)"},
	{ID: DomainSecrets, TablePrefix: "secrets", OwnerPkg: "internal/secrets (Epic H)"},
	{ID: DomainSessions, TablePrefix: "sessions", OwnerPkg: "internal/fleet (sessions, nodes, lanes)"},
	{ID: DomainConfig, TablePrefix: "config", OwnerPkg: "internal/runtime (bootstrap, profiles, config)"},
	{ID: DomainRetrieval, TablePrefix: "retrieval", OwnerPkg: "internal/retrieval"},
	{ID: DomainBlobs, TablePrefix: "blobs", OwnerPkg: "providers/fs (R-14.6)"},
	{ID: DomainQueue, TablePrefix: "queue", OwnerPkg: "internal/storage/queue"},
	{ID: DomainJobs, TablePrefix: "jobs", OwnerPkg: "internal/fleet (task/job dispatch; owning epic not yet finalized beyond R-14.5)"},
	{ID: DomainPolicy, TablePrefix: "policy", OwnerPkg: "internal/policy (Epic I; R-16.51)"},
}

// Clock abstracts time.Now so Bootstrap never reads the wall clock
// directly (forbidigo). Declared locally (duck-typed) rather than
// imported: this is a structural twin of internal/runtime.Clock,
// internal/testkit.Clock, and internal/storage/migrate.Clock — all three
// declare exactly Now() time.Time, so any of their concrete types already
// satisfies this interface with zero adapter code. Production callers pass
// internal/runtime.NewSystemClock(); tests pass internal/testkit's frozen
// clock or a local fake.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// GrantRegistry is the optional self-grant seam Bootstrap calls once per
// domain when non-nil (B/S-02.T5's real GrantRegistry is not yet in-tree).
// Declared locally, duck-typed, for the same reason Clock is: a future
// implementation that declares this one method structurally satisfies this
// interface with no adapter code. Register expresses a domain's
// self-grant only — "domain may always write its own tables" — never a
// cross-domain grant.
type GrantRegistry interface {
	// Register records domain's self-grant. A non-nil error aborts
	// Bootstrap.
	Register(ctx context.Context, domain DomainID) error
}

// BootstrapOpts configures Bootstrap.
type BootstrapOpts struct {
	// Clock supplies the initial schema_version stamp row's applied_at
	// timestamp. Required.
	Clock Clock
	// GrantRegistry, if non-nil, receives one Register call per domain
	// once that domain's anchor table exists. Bootstrap is fully
	// functional with GrantRegistry left nil.
	GrantRegistry GrantRegistry
}

// BootstrapReport summarizes one Bootstrap call: how many domain anchor
// tables this call actually created (0 on every call after the first,
// per the §5.9 idempotency contract) and a human-readable delta.
type BootstrapReport struct {
	TablesCreated int
	Delta         string
}

// bootstrapLedgerTable is the applied_migrations ledger table name, shape-
// compatible with (but not imported from) internal/storage/migrate's
// ledgerDef: id INTEGER PRIMARY KEY AUTOINCREMENT, schema_version INTEGER
// NOT NULL, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL.
const bootstrapLedgerTable = "applied_migrations"

// bootstrapSchemaVersion is the schema_version Bootstrap stamps on a fresh
// database. There is no version below 1 for a bootstrapped database, so
// this doubles as health.go's minimumReaderVersion floor.
const bootstrapSchemaVersion = 1

// bootstrapStampChecksum is the checksum column value Bootstrap's stamp
// row carries. Bootstrap's anchor tables have no migration content to
// checksum (that begins with the first internal/storage/migrate
// MigrationSet a later ticket applies), so this is a fixed sentinel
// rather than a hash of nothing pretending to mean something.
const bootstrapStampChecksum = "bootstrap-stamp"

// healthProbeTable is a reserved, non-domain table Bootstrap creates
// alongside the ten domain anchors, existing solely for
// StorageHealthCheck's probe-write round-trip check (internal/storage/
// health.go) — never one of the R-14.5 ten, and never written to by
// domain logic.
const healthProbeTable = "__health_probe__"

// Bootstrap idempotently creates the eleven domain anchor tables plus the
// reserved health-probe table, sets WAL mode, and stamps the initial
// schema_version row. Calling Bootstrap twice on the same database is a
// no-op on the second call (§5.9): TablesCreated is 0, Delta reports zero,
// and no existing row's data is mutated — see domains_test.go's
// TestBootstrapIdempotent.
func Bootstrap(ctx context.Context, db *sql.DB, opts BootstrapOpts) (BootstrapReport, error) {
	if opts.Clock == nil {
		return BootstrapReport{}, cascade.New(cascade.KindInvalidInput, "storage: Bootstrap requires a non-nil Clock")
	}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL;`); err != nil {
		return BootstrapReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: bootstrap set WAL")
	}

	created, createdNames, err := bootstrapDomainTables(ctx, db, opts)
	if err != nil {
		return BootstrapReport{}, err
	}

	probeCreated, err := createAnchorTable(ctx, db, healthProbeTable)
	if err != nil {
		return BootstrapReport{}, cascade.Wrap(cascade.KindUnavailable, err, "storage: bootstrap health-probe table")
	}
	if probeCreated {
		created++
		createdNames = append(createdNames, healthProbeTable)
	}

	if err := stampSchemaVersion(ctx, db, opts.Clock); err != nil {
		return BootstrapReport{}, err
	}

	return BootstrapReport{TablesCreated: created, Delta: deltaString(createdNames)}, nil
}

// bootstrapDomainTables creates (or confirms present) every AllDomains
// anchor table and calls opts.GrantRegistry.Register for each, in
// AllDomains's deterministic order.
func bootstrapDomainTables(ctx context.Context, db *sql.DB, opts BootstrapOpts) (created int, createdNames []string, err error) {
	for _, meta := range AllDomains {
		table := domainRootTable(meta.TablePrefix)
		wasCreated, cerr := createAnchorTable(ctx, db, table)
		if cerr != nil {
			return 0, nil, cascade.Wrapf(cascade.KindUnavailable, cerr, "storage: bootstrap anchor table for domain %s", meta.ID)
		}
		if wasCreated {
			created++
			createdNames = append(createdNames, string(meta.ID))
		}
		if opts.GrantRegistry != nil {
			if gerr := opts.GrantRegistry.Register(ctx, meta.ID); gerr != nil {
				return 0, nil, cascade.Wrapf(cascade.KindUnavailable, gerr, "storage: register self-grant for domain %s", meta.ID)
			}
		}
	}
	return created, createdNames, nil
}

// domainRootTable returns the canonical anchor-table name for a domain's
// TablePrefix: "<prefix>_domain_root". Every domain's real schema (landing
// in subsequent tickets) lives in additional tables alongside this one;
// the anchor table's only job, as of this ticket, is to exist so
// sqlite_master and StorageHealthCheck have something durable to assert
// against.
func domainRootTable(prefix string) string {
	return prefix + "_domain_root"
}

// deltaString renders createdNames into BootstrapReport.Delta's
// human-readable form.
func deltaString(createdNames []string) string {
	if len(createdNames) == 0 {
		return "no tables created (already bootstrapped)"
	}
	return fmt.Sprintf("created %d table(s): %v", len(createdNames), createdNames)
}
