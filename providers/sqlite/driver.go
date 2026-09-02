// Package sqlite is the modernc-sqlite (pure Go, no CGO) concrete
// provider.Store driver — WAL mode + busy_timeout, backed by a single
// write-connection WriteExecutor (executor.go) so every write in the
// process is serialized through one physical connection regardless of how
// many goroutines call Put/Delete/Tx concurrently.
//
// Purpose: the modernc-sqlite (pure Go, no CGO) concrete provider.Store
//
//	driver — WAL mode + busy_timeout, backed by a single write-connection
//	WriteExecutor (executor.go) so every write in the process is
//	serialized through one physical connection regardless of how many
//	goroutines call Put/Delete/Tx concurrently.
//
// Inputs: a filesystem path to the .db file (Open) plus optional functional
//
//	options (WithSocketProbe for the §D-3 daemon-owns-store check).
//
// Outputs: a *Driver satisfying provider.Store, or a *cascade.Error.
// Constraints: providers/** may import pkg/** only, never internal/**
//
//	(Art.10.2) — so the §D-3 socket-probe step, which in a running daemon
//	would consult internal/rpc's listener state, is an INJECTED function
//	here (SocketProbe) rather than a direct internal/ import; the real
//	probe is wired in by the composition root (cmd/ or internal/daemon),
//	which may import both this package and internal/rpc. Nil probe means
//	"no daemon-liveness check, proceed straight to flock" — the correct
//	default for tests and for callers that already know they are the only
//	process (e.g. storetest's per-test t.TempDir() database).
//
// Domain scoping (P1-E02-W1-S02-T5): the bare *Driver stays unscoped —
// existing callers keep working unchanged. Driver.Scoped (scope.go, split
// under R-14.117's 300-line cap) returns a domain-bound provider.Store
// that enforces the fail-closed cross-domain capability check — the
// "symmetric read guard" alongside executor.go's submitScoped write
// guard — on any call whose namespace differs from the scoped domain.
// GrantChecker (executor.go) is the locally-declared injection seam a
// composition root wires a real internal/storage.CapabilityRegistry into.
//
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2),
//
//	providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T5, domain scoping).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// schemaDDL creates the single key/value table every namespace shares,
// scoped by an explicit namespace column (mirrors provider.Store's
// namespace-scoped method set, storetest's NamespaceIsolation case).
const schemaDDL = `CREATE TABLE IF NOT EXISTS kv (
	namespace TEXT NOT NULL,
	key       TEXT NOT NULL,
	value     BLOB NOT NULL,
	PRIMARY KEY (namespace, key)
) WITHOUT ROWID;`

// SocketProbe reports whether a live daemon already holds path open before
// this process attempts the §D-3 exclusive flock. A nil SocketProbe (the
// Open default) skips the check. Returning true short-circuits Open with
// an actionable ErrDaemonOwnsStore refusal WITHOUT attempting the flock —
// "socket-probe-first" per the ticket's §D-3 arbitration order.
type SocketProbe func(path string) (daemonRunning bool, err error)

// Migrator applies pending schema migrations against the write connection
// before Open returns the Driver, so schema_version is current before any
// caller can read from or write to the store. Injected via WithMigrator —
// exactly the same injection-seam pattern SocketProbe uses one field
// above, for the same reason: providers/sqlite may import pkg/** only,
// never internal/** (Art.10.2, enforced by depguard's
// plugins-providers-boundary rule), so this package never imports
// internal/storage/migrate directly. The real migrator — a thin adapter
// around internal/storage/migrate.Apply — is wired in by the composition
// root (cmd/ or internal/daemon), which is free to import both this
// package and internal/storage/migrate. A nil Migrator (the Open default)
// skips migration entirely, which is correct for any caller that manages
// its own schema (e.g. this package's own storetest-driven tests, which
// exercise the seam directly with a real migrate.Apply adapter — see
// providers/sqlite/README.md "Migration boot path").
type Migrator func(ctx context.Context, db *sql.DB) error

// Driver is the modernc-sqlite provider.Store implementation. One Driver
// owns exactly one .db file, one read *sql.DB (multiple pooled
// connections — WAL allows concurrent readers) and one write *sql.DB
// capped at a single connection, fed exclusively through a WriteExecutor.
type Driver struct {
	path     string
	readDB   *sql.DB
	writeDB  *sql.DB
	exec     *WriteExecutor
	registry *DomainRegistry
	unlock   func() error // §D-3 exclusive flock release, or nil if never acquired
}

// Option configures Open.
type Option func(*openConfig)

type openConfig struct {
	probe    SocketProbe
	migrator Migrator
}

// WithSocketProbe injects the §D-3 daemon-liveness check Open runs before
// attempting the exclusive flock. See SocketProbe's doc for why this is an
// injection point rather than a direct internal/ import.
func WithSocketProbe(p SocketProbe) Option {
	return func(c *openConfig) { c.probe = p }
}

// WithMigrator injects the schema-migration step Open runs, against the
// write connection, immediately after the base kv schema is created and
// before Open returns the Driver. See Migrator's doc comment for why this
// is an injection point rather than a direct internal/storage/migrate
// import.
func WithMigrator(m Migrator) Option {
	return func(c *openConfig) { c.migrator = m }
}

// Open opens (creating if absent) the SQLite database at path in WAL mode
// with a busy_timeout, performs the §D-3 arbitration sequence (socket-probe
// -> exclusive flock -> refuse if either finds a live owner), and returns a
// ready Driver. The caller MUST call Close when done, which releases the
// flock.
func Open(ctx context.Context, path string, opts ...Option) (*Driver, error) {
	cfg := openConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.probe != nil {
		running, err := cfg.probe(path)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: socket probe failed for %s", path)
		}
		if running {
			return nil, ErrDaemonOwnsStore
		}
	}

	// acquireExclusiveLock's three platform implementations (flock_darwin.go
	// / flock_linux.go / flock_windows.go) each return a *cascade.Error
	// with the Kind that already fits their failure (KindConflict for a
	// held lock, KindUnsupported for windows's tier-2 refusal) — propagated
	// as-is rather than re-wrapped, so the caller sees the real reason.
	unlock, err := acquireExclusiveLock(path)
	if err != nil {
		return nil, err
	}

	d, err := openLocked(ctx, path, unlock, cfg.migrator)
	if err != nil {
		_ = unlock()
		return nil, err
	}
	return d, nil
}

// openLocked performs the actual database open + schema/pragma setup once
// the §D-3 flock is held. Split from Open to keep Open's own body under
// Art.10.3's 50-line function cap and to give tests (which materialize
// their own lock) a seam that skips the OS-level flock entirely.
func openLocked(ctx context.Context, path string, unlock func() error, migrator Migrator) (*Driver, error) {
	dsn := "file:" + path + "?" + url.Values{
		"_busy_timeout": {"5000"},
		"_journal_mode": {"WAL"},
		"_foreign_keys": {"1"},
		// Explicitly pinned rather than relied on as modernc-sqlite's
		// compiled-in default (CR nit 1, fix 3): FULL is what makes a
		// committed write durable across a power cut, not just an app
		// crash (see README.md "Durability"). Pinning it means a future
		// dependency upgrade or a well-meaning perf change to this DSN
		// cannot silently drop to NORMAL with nothing noticing — a live
		// PRAGMA assertion in durability_internal_test.go backs this up.
		"_synchronous": {"FULL"},
	}.Encode()

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: open read pool %s", path)
	}
	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = readDB.Close()
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: open write connection %s", path)
	}
	writeDB.SetMaxOpenConns(1) // single write-connection invariant

	if _, err := writeDB.ExecContext(ctx, schemaDDL); err != nil {
		_ = readDB.Close()
		_ = writeDB.Close()
		return nil, wrapDBError(err, "sqlite: schema init %s", path)
	}

	if migrator != nil {
		if err := migrator(ctx, writeDB); err != nil {
			_ = readDB.Close()
			_ = writeDB.Close()
			return nil, err
		}
	}

	d := &Driver{
		path:     path,
		readDB:   readDB,
		writeDB:  writeDB,
		exec:     newWriteExecutor(writeDB),
		registry: newDomainRegistry(),
		unlock:   unlock,
	}
	return d, nil
}

// Close stops the write executor, closes both connection pools, and
// releases the §D-3 exclusive flock. Close is idempotent.
func (d *Driver) Close() error {
	d.exec.close()
	errRead := d.readDB.Close()
	errWrite := d.writeDB.Close()
	var errLock error
	if d.unlock != nil {
		errLock = d.unlock()
		d.unlock = nil
	}
	for _, e := range []error{errRead, errWrite, errLock} {
		if e != nil {
			return cascade.Wrap(cascade.KindUnavailable, e, "sqlite: close")
		}
	}
	return nil
}

// Get returns the value stored under key in namespace.
func (d *Driver) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	return getValue(ctx, d.readDB, namespace, key)
}

// Put writes value under key in namespace via the write executor, fairness-
// tagged by namespace.
func (d *Driver) Put(ctx context.Context, namespace, key string, value []byte) error {
	return d.exec.submit(ctx, namespace, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO kv (namespace, key, value) VALUES (?, ?, ?)
			ON CONFLICT (namespace, key) DO UPDATE SET value = excluded.value`, namespace, key, value)
		if err != nil {
			return wrapDBError(err, "sqlite: put %s/%s", namespace, key)
		}
		return nil
	})
}

// Delete removes key from namespace via the write executor. Deleting an
// absent key is not an error (idempotent, per provider.Store's contract).
func (d *Driver) Delete(ctx context.Context, namespace, key string) error {
	return d.exec.submit(ctx, namespace, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE namespace = ? AND key = ?`, namespace, key); err != nil {
			return wrapDBError(err, "sqlite: delete %s/%s", namespace, key)
		}
		return nil
	})
}

// Scan returns an Iterator over every key in namespace with the given
// prefix, in key order.
func (d *Driver) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	return newScanIterator(ctx, d.readDB, namespace, prefix)
}

// Tx runs fn inside one atomic write-executor job: the whole closure
// executes as a single *sql.Tx against the write connection, fairness-
// tagged under the reserved "tx" slot (DESIGN DECISION: provider.Store.Tx
// carries no namespace argument, so the fairness key cannot be known before
// fn runs and may span several namespaces — see providers/sqlite/README.md
// "Tx fairness" for the full rationale).
func (d *Driver) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	return d.exec.submit(ctx, txFairnessDomain, func(sqlTx *sql.Tx) error {
		return fn(ctx, &driverTx{ctx: ctx, sqlTx: sqlTx})
	})
}

// txFairnessDomain is the reserved fairness-queue slot every Tx job shares,
// distinct from any real namespace (namespaces are caller-chosen strings;
// this sentinel is deliberately not one storetest or a real domain would
// pick, but collision is harmless either way since it only affects
// scheduling order, never correctness).
const txFairnessDomain = "\x00tx"

var _ provider.Store = (*Driver)(nil)

// String is a debug-only human label (path only, never opens a connection).
func (d *Driver) String() string { return fmt.Sprintf("sqlite.Driver(%s)", d.path) }
