package plugins

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/internal/client"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	reviewengine "github.com/acamarata/cascade/internal/review"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	reviewplugin "github.com/acamarata/cascade/plugins/review"
)

// Purpose (this file): the cascade-review composition root -- the bounded
//
//	host bridge P1-EXEC-20260918 explicitly approves (S52T4's resolved
//	alias: "bounded host bridge planned ADD internal/plugins/
//	review_wiring.go plus cmd composition injection... using pkg-level
//	interface; root approval P1-EXEC-20260918"). plugins/review may not
//	import internal/** (Art.10.2); this package -- the one place allowed
//	to import both sides -- constructs the real internal/review engine
//	and injects it via reviewplugin.SetReviewProvider, exactly as
//	cascadepa_wiring.go bridges `cascade chat` to the daemon transport and
//	claude_wiring.go bridges cascade-claude's Generate seam.
//
// Inputs: none at import time. Every collaborator resolves lazily, per
//
//	call, never at init() -- the same deferred-resolution discipline
//	cascadepa_wiring.go's pathResolver documents, so importing this
//	package never touches the environment.
//
// Outputs: registers a real *review.Provider with plugins/review via
//
//	SetReviewProvider. Every dispatch reaches the daemon's real
//	"conductor.execute" door through pkg/provider.Client.ModelExecute
//	over internal/client (the SAME seam cmd/cascade/run_exec.go's fetchRun
//	dials for `cascade run`) -- never a direct provider call. The family-
//	eligibility check reads the SAME durable providers.db
//	`provider add/list/health` and the daemon's own K/S-22 conductor
//	wiring use (cmd/cascade/provider_health_cmd.go's providerRegistryDBFile,
//	cmd/cascade/daemon_unix_conductor.go's wireConductorExecute), through
//	registry.NewReader -- the real, already-built pkg/provider.
//	ProviderRegistryReader adapter (registry/lanes.go, J/S-19/J/S-20) --
//	never a hand-rolled query.
//
// Constraints: opened and closed PER CALL, not held for the process
//
//	lifetime: this package's init() must never touch the environment, and
//	no other part of this composition root hands this file a long-lived
//	*sql.DB to own closing. A registry-open/migration failure is a real,
//	typed error, never silently treated as "zero families registered".
//
// SPORT: internal/plugins:review-wiring (ADD) -- P1-E25-W5-S52-T4
// (execution_guidance P1-EXEC-20260918, bounded host bridge, root
// approval).

// reviewClientTimeout bounds every conductor.execute round trip a review
// dispatch issues. Longer than cmd/cascade/status.go's statusDialTimeout
// precedent (a status probe): a CR-B/CR-C dispatch is a real model call,
// not a liveness check.
const reviewClientTimeout = 30 * time.Second

// reviewProvidersDBFile matches cmd/cascade/provider_health_cmd.go's own
// providerRegistryDBFile constant -- the same durable providers.db, never
// a second file.
const reviewProvidersDBFile = "providers.db"

func init() {
	installReviewProvider(newRealReviewProvider)
}

// installReviewProvider is init()'s body, pulled out so
// review_wiring_test.go can drive the panic branch through a
// deliberately-failing constructor -- the "impossible construction
// failure" it guards against for the real newRealReviewProvider is exactly
// as unreachable from inside init() itself as it always was; this is a pure
// extraction, never a behavior change.
func installReviewProvider(newProvider func() (*reviewengine.Provider, error)) {
	p, err := newProvider()
	if err != nil {
		// NewProvider only fails on a nil collaborator, which cannot
		// happen from the two literal constructions in
		// newRealReviewProvider -- the same "impossible construction
		// failure" reasoning pkg/plugin/register.go's own doc comment
		// gives for init-time invariants.
		panic("internal/plugins: cascade-review provider construction: " + err.Error())
	}
	_ = reviewplugin.SetReviewProvider(p)
}

// newRealReviewProvider builds the exact review.Provider init() installs,
// from the real production collaborators (client.UnixDialer,
// runtime.NewDefaultPathProvider, runtime.NewSystemClock) -- split out so
// review_wiring_test.go exercises the SAME construction init() runs,
// rather than a re-typed duplicate.
func newRealReviewProvider() (*reviewengine.Provider, error) {
	return reviewengine.NewProvider(
		newReviewModelExecutor(client.UnixDialer, reviewClientTimeout, runtime.NewDefaultPathProvider),
		reviewRegistryReader{resolvePaths: runtime.NewDefaultPathProvider, clock: runtime.NewSystemClock()},
		reviewSlogEvents{},
	)
}

// reviewSlogEvents is the PRODUCTION reviewengine.EventPublisher: the
// destination the "same-family fallback is used and LOGGED, never silent"
// acceptance criterion needs. internal/runtime's own LogProvider cannot be
// constructed from here (NewLogProvider takes runtime's unexported logging
// config section), and no other wiring file in this package carries a logger
// or journal seam -- verified by grep before choosing -- so this writes one
// structured slog line through the process default handler. Every field is a
// code-chosen string or a family name; no artifact content, model output or
// author identity is ever logged (R-21.156 blindness covers the telemetry).
type reviewSlogEvents struct{}

// Publish implements reviewengine.EventPublisher.
func (reviewSlogEvents) Publish(ctx context.Context, event reviewengine.Event) {
	if event.Fallback == nil {
		return
	}
	fb := event.Fallback
	slog.Default().WarnContext(ctx, "cascade-review: same-family reviewer fallback",
		slog.String("level", string(fb.Level)),
		slog.String("consequence_class", string(fb.ConsequenceClass)),
		slog.Any("families", fb.Families),
		slog.String("reason", fb.Reason),
	)
}

// reviewModelExecutor adapts pkg/provider.Client's ModelExecute (the
// D/S-07.T3 client's typed wrapper for the daemon's real "conductor.
// execute" door) to provider.ModelExecutor's Execute shape -- the SAME
// production seam cmd/cascade/run_exec.go's fetchRun dials for `cascade
// run`, and the same pathResolver/rpcDoer pattern cascadepa_wiring.go's
// cascadePAClient already declares in this package.
type reviewModelExecutor struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
	// doer, when non-nil, replaces the real rpcClient() construction --
	// see cascadepa_wiring.go's rpcDoer doc comment. Always nil in
	// production.
	doer rpcDoer
}

func newReviewModelExecutor(dial client.DialFunc, timeout time.Duration, resolvePaths pathResolver) *reviewModelExecutor {
	return &reviewModelExecutor{dial: dial, timeout: timeout, resolvePaths: resolvePaths}
}

func (e *reviewModelExecutor) rpcClient() (rpcDoer, error) {
	if e.doer != nil {
		return e.doer, nil
	}
	paths, err := e.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade-review: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), e.dial, e.timeout), nil
}

// Execute implements provider.ModelExecutor: exactly one
// conductor.execute round trip, decoded through pkg/provider.Client.
func (e *reviewModelExecutor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	rpc, err := e.rpcClient()
	if err != nil {
		return provider.ModelResponse{}, err
	}
	return provider.NewClient(rpc).ModelExecute(ctx, req)
}

// reviewRegistryReader implements provider.ProviderRegistryReader by
// lazily opening the durable providers.db, migrating it, and delegating
// through registry.NewReader -- the real production adapter K/S-22's own
// conductor wiring (cmd/cascade/daemon_unix_conductor.go) uses. Opened and
// closed per call; see this file's own header comment for why.
type reviewRegistryReader struct {
	resolvePaths pathResolver
	clock        migrate.Clock
}

// open resolves the data directory, opens providers.db and migrates it,
// returning the ready *sql.DB and its closer. The caller MUST call the
// returned closer, even on a later error from its own use of db.
func (r reviewRegistryReader) open(ctx context.Context) (db *sql.DB, closeFn func(), err error) {
	paths, err := r.resolvePaths()
	if err != nil {
		return nil, func() {}, cascade.Wrap(cascade.KindUnavailable, err, "cascade-review: resolve data directory")
	}
	dbPath := filepath.Join(paths.DataDir(), reviewProvidersDBFile)
	db, err = sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return nil, func() {}, cascade.Wrapf(cascade.KindUnavailable, err, "cascade-review: open %s", dbPath)
	}
	db.SetMaxOpenConns(1)
	if err := providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, r.clock, "", ""); err != nil {
		_ = db.Close()
		return nil, func() {}, err
	}
	return db, func() { _ = db.Close() }, nil
}

// reader builds a fresh registry.Reader over a freshly opened db -- one
// per call, matching this type's own per-call lifecycle.
func (r reviewRegistryReader) reader(ctx context.Context) (*providerregistry.Reader, func(), error) {
	db, closeFn, err := r.open(ctx)
	if err != nil {
		return nil, func() {}, err
	}
	return providerregistry.NewReader(providerregistry.NewRegistry(db, r.clock)), closeFn, nil
}

func (r reviewRegistryReader) GetProvider(ctx context.Context, name string) (provider.ProviderInfo, error) {
	rd, closeFn, err := r.reader(ctx)
	if err != nil {
		return provider.ProviderInfo{}, err
	}
	defer closeFn()
	return rd.GetProvider(ctx, name)
}

func (r reviewRegistryReader) ListProviders(ctx context.Context) ([]provider.ProviderInfo, error) {
	rd, closeFn, err := r.reader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return rd.ListProviders(ctx)
}

func (r reviewRegistryReader) ListLanes(ctx context.Context) ([]provider.LaneInfo, error) {
	rd, closeFn, err := r.reader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return rd.ListLanes(ctx)
}

func (r reviewRegistryReader) ListPool(ctx context.Context, pool string) ([]provider.LaneInfo, error) {
	rd, closeFn, err := r.reader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return rd.ListPool(ctx, pool)
}

func (r reviewRegistryReader) GetByModel(ctx context.Context, model string) ([]provider.ProviderInfo, error) {
	rd, closeFn, err := r.reader(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return rd.GetByModel(ctx, model)
}
