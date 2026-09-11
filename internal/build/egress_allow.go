// Package build (this file) carries the egress import allowlists the A-T2
// arch gate enforces: which packages may import the network stdlib, and
// which may spawn a process.
//
// The two lists are SEPARATE and are never merged. Network I/O is egress:
// bytes leave the machine, so every network importer must be a package
// that goes through the egress firewall. Spawning a process is NOT
// egress: it is bound by the driver-boundary rules instead, and folding
// it into the network list would let a `net/http` importer hide behind a
// process-spawn exemption.
//
// Each list has two halves. The NORMATIVE half is the set the egress
// ruling names; it is the target state, and an entry is added to it only
// by a ruling. The NOT-YET-MIGRATED half is what the tree actually holds
// today, each entry carrying the reason it is still there. Splitting them
// is what keeps the gate honest: it turns red on a NEW unlisted importer
// while naming, rather than hiding, the ones already present.
package build

// EgressAllowEntry is one allowlisted package directory.
type EgressAllowEntry struct {
	// Dir is the module-relative package directory, e.g.
	// "internal/secrets". A trailing "/*" admits the directory and every
	// package beneath it.
	Dir string
	// Reason states why this package is allowed to import what it does.
	Reason string
}

// EgressNetNormative is the set of packages the egress ruling names as
// permitted importers of "net" and "net/http".
var EgressNetNormative = []EgressAllowEntry{
	{"internal/secrets", "holds the in-package egress door the token-endpoint call transits under the oauth class"},
	{"internal/providers/intake", "the provider-intake fetch; the health and recovery probes reuse its class"},
	{"internal/providers/health", "provider health probes"},
	{"providers/*", "a provider talks to its vendor endpoint; that is what a provider is"},
	{"internal/backup/targets", "a remote backup destination"},
	{"internal/plugins/remote", "the remote plugin runtime, disabled by default"},
	{"internal/plugins/registryfetch", "the plugin registry fetch"},
	{"internal/nodes", "node dispatch transport"},
	{"internal/sync", "the sync engine"},
	{"plugins/*", "bridge plugins reach their own services"},
}

// EgressNetNotYetMigrated is what the tree holds today outside the
// normative set. Every entry is a package that predates the egress ruling
// and has not been routed through the firewall yet. The list exists so
// that these are NAMED rather than silently tolerated, and so that a new
// unlisted importer still turns the gate red.
var EgressNetNotYetMigrated = []EgressAllowEntry{
	{"cmd/cascade", "the composition root builds the client and daemon transports"},
	{"internal/build", "the gates themselves parse import paths as data; they open no socket"},
	{"internal/client", "the control-socket client; local transport, not egress"},
	{"internal/daemon", "the control-socket listener; local transport, not egress"},
	{"internal/rpc", "the control-plane RPC frame layer; local transport, not egress"},
	{"internal/runtime", "address and socket-path helpers"},
	// NOTE ON THIS ENTRY'S PLACEMENT. internal/fleet/sessions does not
	// "predate the egress ruling" the way every entry above it does: it
	// was added after. It is here because it is the same KIND of importer
	// as internal/rpc and internal/daemon -- it serves an SSE stream over
	// the local control socket, so no bytes leave the machine and there is
	// nothing for the egress firewall to filter. That class will never
	// "migrate", because it was never egress; the list name is inaccurate
	// for it, and splitting local-transport out into its own list is a
	// change worth making deliberately rather than as a side effect of
	// landing a ticket. Recorded so the imprecision is visible instead of
	// buried in a one-line append.
	{"internal/fleet/sessions", "the fleet.sessions SSE stream over the local control socket; local transport, not egress (see note above)"},
}

// EgressExecNormative is the set the ruling names as permitted importers
// of "os/exec". Process spawn is not egress; this list is bound by the
// driver-boundary rules.
var EgressExecNormative = []EgressAllowEntry{
	{"internal/plugins/process", "the process plugin driver, the one place a plugin subprocess is started"},
}

// EgressExecNotYetMigrated is what the tree holds today outside that set.
var EgressExecNotYetMigrated = []EgressAllowEntry{
	// NOTE ON THIS ENTRY'S PLACEMENT. internal/backup/targets does not
	// "predate" the process-spawn ruling the way every entry below it
	// does -- the ruling's own egressExecSpec names no member for it. It
	// is here rather than on the normative list because R-21.265/§D-15
	// name the rclone target as exec-only (no rclone Go-library linkage)
	// but no ruling text adds it to THIS list; the process it spawns
	// (rclone) is bound by the driver-boundary rules this file's own doc
	// comment describes, not by the egress firewall (bytes still transit
	// Intercept via egress.go before rclone ever sees them). Recorded
	// here, with a reason, the same treatment internal/fleet/sessions
	// already got for the identical kind of gap.
	{"internal/backup/targets", "the rclone backup target's exec-only invocation of the rclone binary (P1-E19-W4-S41-T3, §D-15); outbound bytes still transit Intercept via egress.go before rclone is invoked"},
	{"cmd/cascade", "the composition root runs the operator's own configured commands"},
	{"cmd/cascade/config", "opens the operator's editor"},
	{"internal/build", "the gates shell out to the toolchain to inspect the tree"},
	{"internal/context", "runs the tree-walking helpers the context builder uses"},
	{"internal/daemon", "starts and stops the daemon process"},
	{"internal/daemon/service", "installs the platform service definition"},
	{"internal/doctor", "probes the toolchain the operator has installed"},
	{"internal/inventory", "shells out to `git ls-files`/`git rev-parse` to derive tree-wide counts (providers, plugins, SPORT lines) at generation time; no subprocess in the installed binary's own request path"},
	{"internal/retrieval", "the code-corpus git counterpart (P1-E25-W5-S52-T6): GitTrackedFiles shells out to `git ls-files -z` to enumerate a repository's tracked files, the R-14.80 decision that git is already an Art.2 external contract exercised in tests, not a new module dependency"},
	{"internal/inventory/gen", "the counts.json generator; resolves the repo root via `git rev-parse --show-toplevel`, same class as internal/inventory above"},
	{"internal/inventory/sport", "shells out to `git ls-files` to derive the tracked .go file list at SPORT-registry generation time only (ComputeRegistry/tree.go); the production binary reads the embedded registry.json instead (embed.go) and never spawns this"},
	{"internal/secrets", "reads a custody backend through its platform command-line tool"},
	{"internal/syncmerge", "P1-E13-W3-S27-T5 spike: phaseMergeGit shells out to the real git binary to exercise the Git external contract (Art.2); every merge function in this package, including this one, is guarded by the spike build tag and ships in no release binary, and Q/S-38.T2 deletes the file when its production engine lands"},
}

// EgressNetImports are the stdlib import paths the network list governs.
var EgressNetImports = []string{"net", "net/http"}

// EgressExecImports are the stdlib import paths the process list governs.
var EgressExecImports = []string{"os/exec"}

// egressAllowDirs flattens entries into their directory patterns.
func egressAllowDirs(lists ...[]EgressAllowEntry) []string {
	var out []string
	for _, list := range lists {
		for _, entry := range list {
			out = append(out, entry.Dir)
		}
	}
	return out
}

// EgressDirAllowed reports whether dir is admitted by any pattern. A
// pattern ending in "/*" admits the parent directory and everything under
// it; every other pattern matches exactly.
func EgressDirAllowed(dir string, patterns []string) bool {
	for _, pattern := range patterns {
		if egressPatternMatches(dir, pattern) {
			return true
		}
	}
	return false
}
