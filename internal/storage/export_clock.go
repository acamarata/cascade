package storage

import "github.com/acamarata/cascade/internal/runtime"

// Purpose: the clock-injection seam Export's header (ExportHeader.
//
//	ExportedAt) uses. Split into its own file because it is a genuinely
//	distinct concern from export.go's wire types/logic — a global,
//	mutable package variable is unusual enough in this codebase's
//	otherwise-uniform "Clock arrives via an explicit Opts field"
//	convention (domains.go's BootstrapOpts.Clock, retention.go's
//	RetentionConfig.Clock) that it deserves its own file and its own,
//	explicit justification rather than being buried in export.go.
//
// Why this ticket cannot follow the BootstrapOpts.Clock convention:
// Export's signature is contract-fixed by this ticket's own T-3.yaml task
// text — "Export(ctx, db, domain DomainID, w io.Writer) error" — with no
// options parameter to carry a Clock. A package-level variable is the only
// injection point left that still (a) never calls time.Now() directly
// inside export.go itself (satisfying forbidigo + internal/build's AST
// clock gate, which matches the bare selector regardless of which
// function wraps it) and (b) still lets tests freeze the value
// deterministically (Art.7.3). This mirrors exactly what BootstrapOpts.
// Clock and RetentionConfig.Clock delegate to underneath — a
// runtime.Clock value whose Now() implementation lives in one of the two
// files internal/build/clockgate.go's exemption list allows to call
// time.Now() bare — just injected at package-init time instead of at each
// call site, because the call site (Export's fixed signature) has no room
// for it.
//
// SPORT: internal.storage.export.Export/ADDED (P1-E02-W1-S03-T3).

// exportClock supplies Export's ExportedAt header field. Defaults to the
// real wall clock (internal/runtime.NewSystemClock — its Now()
// implementation lives in internal/runtime/clock.go, one of the two files
// internal/build/clockgate.go's AST gate exempts from the bare-time.Now
// rule; every other call in this package, including this one, only ever
// calls a Clock's Now() method through this variable). Every determinism-
// sensitive test in this package (TestExportDeterminism, TestExportGolden,
// TestImportRoundTrip) overrides it via SetExportClock before calling
// Export and restores it before returning, so no test observes wall-clock
// jitter.
var exportClock Clock = runtime.NewSystemClock()

// SetExportClock overrides exportClock and returns a restore function the
// caller must invoke (typically via defer) to put the previous clock back.
// Exported — not test-only — because internal/storage's own test suite is
// entirely black-box (package storage_test, matching every existing
// domains_*_test.go/health_*_test.go file), so a test in that package
// cannot reach an unexported package variable directly; this function is
// the seam it uses instead. Not goroutine-safe against a concurrent Export
// call on another goroutine — callers must not run an affected test under
// t.Parallel() (none in this ticket's suite do).
func SetExportClock(c Clock) (restore func()) {
	prev := exportClock
	exportClock = c
	return func() { exportClock = prev }
}
