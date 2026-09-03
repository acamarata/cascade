// Package runtime (daemonless.go): Purpose: the headless embedded runtime (§D-3/§D-24, P1-E04-W1-S07-T4).
//   Socket-probe auto-fallback, EmbeddedMode carried on a context, and
//   §D-3 write-arbitration for the daemonless path.
// Inputs: a unix socket path + Dialer (probe), a cascade.db path
//   (arbitration).
// Outputs: DaemonlessState (embedded yes/no), a *sqlite.Driver for writes
//   (full arbitration) or reads (read-only fallback when the daemon owns
//   the store).
// Constraints: reuses probeSocket (recovery_scan.go) verbatim rather than
//   defining a second probe — same package, same semantics. Reuses
//   providers/sqlite.Open for ALL write-path arbitration (socket-probe +
//   exclusive flock + ErrDaemonOwnsStore already live there); this file
//   adds no second flock implementation. No bare time.Now/time.Sleep.
// SPORT: internal/runtime EmbeddedRuntime/ADDED.
package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// DaemonlessState is the socket-probe auto-fallback's result: whether this
// process should run the embedded (daemonless) path. There is no CLI flag
// for this (07-CLI-COMMAND-TREE's global-flag set is fixed) — the probe is
// the only activation path, per §D-3.
type DaemonlessState struct {
	// Embedded is true unless the probe CONFIRMED a live daemon on
	// SocketPath. An undecidable probe result (permission denied, etc.)
	// also falls back to Embedded=true — ProbeErr carries the reason so
	// the caller can surface a notice, but a process that cannot prove a
	// daemon is listening must not block a one-shot command on that
	// uncertainty.
	Embedded bool
	// SocketPath is the socket this state was probed against.
	SocketPath string
	// ProbeErr is non-nil only for the undecidable case described above.
	ProbeErr error
}

type daemonlessCtxKey struct{}

// WithDaemonlessState attaches st to ctx — the "root Runtime context" the
// contract names; cmd/cascade/root.go's PersistentPreRunE calls this once,
// on cmd.Context(), for every invocation.
func WithDaemonlessState(ctx context.Context, st DaemonlessState) context.Context {
	return context.WithValue(ctx, daemonlessCtxKey{}, st)
}

// DaemonlessStateFrom reads back the state WithDaemonlessState attached.
// ok is false when the root command never ran the probe (e.g. a unit test
// constructing its own bare context) — callers must treat that as "unknown,
// not embedded" rather than assume either mode.
func DaemonlessStateFrom(ctx context.Context) (DaemonlessState, bool) {
	st, ok := ctx.Value(daemonlessCtxKey{}).(DaemonlessState)
	return st, ok
}

// ProbeDaemonless runs the §D-3 socket-probe and returns the resulting
// DaemonlessState. It is a thin wrapper over probeSocket (recovery_scan.go)
// — the SAME function recovery scanning uses — not a second probe: "live"
// there is exactly "a listener answered," which is what daemonless
// activation needs. socketPath/timeout/dial mirror probeSocket's own
// parameters; a nil dial uses the real net.Dial-backed default the
// recovery scanner's production callers already inject.
func ProbeDaemonless(socketPath string, timeout time.Duration, dial Dialer) DaemonlessState {
	if dial == nil {
		dial = defaultDialer
	}
	live, _, err := probeSocket(socketPath, timeout, dial)
	return DaemonlessState{Embedded: !live, SocketPath: socketPath, ProbeErr: err}
}

// ElevationPrecondition reports the two daemonless-elevation facts (§D-24):
// whether a helper key is enrolled (internal/elevation's
// ElevationTrustStore) and whether a local authenticator is available
// (internal/elevation's ElevationKeystore.IsAvailable). It is an
// injection seam, not a direct call: internal/elevation imports
// internal/runtime (ElevationTrustStore's Clock/Backend types), so the
// reverse import here would cycle. The real function is wired in by the
// composition root (cmd/cascade), which may import both packages; a nil
// ElevationPrecondition means "cannot determine — fail closed," see
// DaemonlessElevationPrecondition's doc comment.
type ElevationPrecondition func() (helperEnrolled, authenticatorAvailable bool)

// DaemonlessElevationPrecondition evaluates fn, or fails closed (false,
// false) when fn is nil — R-14.163: a caller that has not wired the real
// composition-root check must never be answered "available."
func DaemonlessElevationPrecondition(fn ElevationPrecondition) (helperEnrolled, authenticatorAvailable bool) {
	if fn == nil {
		return false, false
	}
	return fn()
}

// ErrDaemonOwnsStore is the §D-3 write-arbitration refusal a WRITE verb
// surfaces when the exclusive flock (or the socket probe inside
// providers/sqlite.Open) proves the daemon (or another embedded process)
// currently owns cascade.db. It wraps providers/sqlite.ErrDaemonOwnsStore
// (errors.Is against either sentinel succeeds) with the actionable hint
// the contract requires, rather than a second, differently-worded error.
func ErrDaemonOwnsStore(cause error) error {
	if cause == nil {
		cause = sqlite.ErrDaemonOwnsStore
	}
	return cascade.Wrap(cascade.KindConflict, cause,
		`runtime: daemon owns the store; run "cascade daemon status" or "cascade daemon start" and retry`)
}

// OpenEmbeddedWriteStore opens path for a WRITE verb in embedded mode. It
// performs NO flock logic of its own: providers/sqlite.Open already runs
// the full §D-3 sequence (socket-probe, if probe is non-nil, then the
// exclusive OS flock) — this function only classifies the failure. A
// flock/probe conflict never proceeds; it returns ErrDaemonOwnsStore and
// the caller must exit non-zero without having touched the store.
func OpenEmbeddedWriteStore(ctx context.Context, path string, probe sqlite.SocketProbe, migrator sqlite.Migrator) (*sqlite.Driver, error) {
	opts := []sqlite.Option{}
	if probe != nil {
		opts = append(opts, sqlite.WithSocketProbe(probe))
	}
	if migrator != nil {
		opts = append(opts, sqlite.WithMigrator(migrator))
	}
	d, err := sqlite.Open(ctx, path, opts...)
	if err != nil {
		if errors.Is(err, sqlite.ErrDaemonOwnsStore) {
			return nil, ErrDaemonOwnsStore(err)
		}
		return nil, err
	}
	return d, nil
}

// OpenEmbeddedReadStore opens path for a READ verb in embedded mode. §D-3:
// "a flock conflict is never a failure for a read." It first tries the
// same full-arbitration sqlite.Open a write would use (the common,
// no-daemon-running case: this gets a normal read/write Driver, closed by
// the caller after the read like any other embedded process). Only when
// that reports ErrDaemonOwnsStore does it fall back to readOnlyStore, a
// genuine read-only connection that never contends for the exclusive
// flock at all.
//
// KNOWN GAP (reported, not papered over): providers/sqlite exposes no
// read-only Open variant, and this ticket's files_scope does not include
// providers/sqlite (only internal/runtime and internal/policy are
// writable here) — adding one is the correct long-term fix but is out of
// this ticket's scope. readOnlyStore below is the narrowest workaround
// that does not touch providers/sqlite: a second, independent
// database/sql connection opened with SQLite's own "mode=ro" query
// parameter (a standard modernc-sqlite/SQLite feature, not a
// reimplementation of providers/sqlite's logic) against the same kv
// schema providers/sqlite.Open creates. It is READ-ONLY at the SQLite
// level (SQLITE_OPEN_READONLY) — Put/Delete/Tx on it fail closed with
// KindPermissionDenied rather than silently succeeding.
func OpenEmbeddedReadStore(ctx context.Context, path string, probe sqlite.SocketProbe) (provider.Store, func() error, error) {
	d, err := OpenEmbeddedWriteStore(ctx, path, probe, nil)
	if err == nil {
		return d, d.Close, nil
	}
	if !errors.Is(err, sqlite.ErrDaemonOwnsStore) {
		return nil, nil, err
	}
	ro, roErr := openReadOnlyStore(ctx, path)
	if roErr != nil {
		return nil, nil, roErr
	}
	return ro, ro.close, nil
}

// readOnlyStore is the documented-gap workaround provider.Store
// implementation described on OpenEmbeddedReadStore. It reads the SAME kv
// schema providers/sqlite.Open creates ("CREATE TABLE IF NOT EXISTS kv
// (namespace, key, value)" — the ONLY table cascade.db has anywhere in
// this tree today per providers/sqlite/driver.go's schemaDDL) through a
// read-only *sql.DB.
type readOnlyStore struct {
	db *sql.DB
}

func openReadOnlyStore(ctx context.Context, path string) (*readOnlyStore, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: open read-only store %s", path)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: read-only store %s unreachable", path)
	}
	return &readOnlyStore{db: db}, nil
}

func (s *readOnlyStore) close() error { return s.db.Close() }

func (s *readOnlyStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE namespace = ? AND key = ?`, namespace, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, cascade.Newf(cascade.KindNotFound, "runtime: read-only store: %s/%s not found", namespace, key)
	}
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: read-only store: get %s/%s", namespace, key)
	}
	return v, nil
}

func (s *readOnlyStore) Put(context.Context, string, string, []byte) error {
	return cascade.New(cascade.KindPermissionDenied, "runtime: read-only store: write refused (daemon owns the store)")
}

func (s *readOnlyStore) Delete(context.Context, string, string) error {
	return cascade.New(cascade.KindPermissionDenied, "runtime: read-only store: delete refused (daemon owns the store)")
}

func (s *readOnlyStore) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM kv WHERE namespace = ? AND key >= ? AND key < ? ORDER BY key`,
		namespace, prefix, prefixUpperBound(prefix))
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: read-only store: scan %s/%s*", namespace, prefix)
	}
	return &readOnlyIterator{rows: rows}, nil
}

func (s *readOnlyStore) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return cascade.New(cascade.KindPermissionDenied, "runtime: read-only store: transaction refused (daemon owns the store)")
}

// prefixUpperBound returns the smallest key strictly greater than every
// key sharing prefix, so `key >= prefix AND key < upperBound` emulates a
// prefix scan without a LIKE clause. An empty prefix (scan-everything)
// returns "" (unbounded upper edge, sentinel handled by the caller's SQL
// as "everything" since every real key is < "" is false — guarded below).
func prefixUpperBound(prefix string) string {
	if prefix == "" {
		return "\xff\xff\xff\xff" // no real key sorts >= this
	}
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0xff {
			b[i]++
			return string(b[:i+1])
		}
	}
	return "\xff\xff\xff\xff"
}

type readOnlyIterator struct {
	rows *sql.Rows
	key  string
	val  []byte
	err  error
}

func (it *readOnlyIterator) Next(context.Context) bool {
	if it.err != nil || !it.rows.Next() {
		return false
	}
	if err := it.rows.Scan(&it.key, &it.val); err != nil {
		it.err = err
		return false
	}
	return true
}

func (it *readOnlyIterator) Key() string  { return it.key }
func (it *readOnlyIterator) Value() []byte { return it.val }
func (it *readOnlyIterator) Err() error {
	if it.err != nil {
		return it.err
	}
	return it.rows.Err()
}
func (it *readOnlyIterator) Close() error { return it.rows.Close() }
