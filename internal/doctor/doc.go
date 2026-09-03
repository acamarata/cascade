// Package doctor implements the pluggable diagnostic framework behind
// `cascade doctor` and `cascade doctor bundle` (P1-E03-W1-S05-T2).
//
// Purpose: every subsystem registers a Check (check.go) with a
//
//	CheckRegistry (registry.go); the bounded-concurrency Runner
//	(runner.go) executes every registered check (or only firstRun=true
//	checks, under --first-run), collects a RunReport, and optionally
//	calls Fix on every fixable check that reported warn/error
//	(--fix). handler.go renders a RunReport for the TTY, non-TTY, and
//	--json surfaces and forward-stubs the `cascade doctor`/`cascade
//	doctor bundle` cobra wiring (D/S-06.T1 owns the real cobra root —
//	see handler.go's CASCADE-ALLOW comment). bundle.go packages a
//	redacted gzip-tar diagnostic artifact; redact.go is the
//	RECALL-FIRST detector (§D-31) that bundle.go runs over every value
//	before it is written.
//
// Composition pattern (Art.1 "compose, don't reimplement"): a subsystem
//
//	that already has a real, probing health function — e.g.
//	internal/storage's StorageHealthCheck, which returns a
//	storage.HealthReport of five genuinely-executed probes — wraps that
//	function in a small Check adapter (Name/Describe/Metadata plus a
//	Run that calls the existing function and maps its per-probe
//	CheckResult.OK to this package's Status) rather than re-deriving
//	the same probes here. This ticket ships the framework and two
//	framework-owned checks (mcpcheck.go, census.go); subsystem adapters
//	(a storage adapter included) are out of files_scope for this
//	ticket and are registered by the owning subsystem's own package at
//	composition-root init time, per the ticket contract's task 6.
//
// Constraints: context.Context is the first argument on every crossing
//
//	function; no bare time.Now()/time.Since() in non-test code — every
//	timeout is driven off an injected internal/runtime.Clock or a
//	context deadline set by the caller; every test writes only under
//	t.TempDir() (Art.7.1) and performs no network I/O (Art.7.2).
//
// SPORT: placeholder: doctor/framework (ADD) — see T-2 sport_updates;
//
//	placeholder: doctor/redact (ADD); placeholder: doctor/bundle (ADD).
package doctor
