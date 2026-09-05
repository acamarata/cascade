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
}

// EgressExecNormative is the set the ruling names as permitted importers
// of "os/exec". Process spawn is not egress; this list is bound by the
// driver-boundary rules.
var EgressExecNormative = []EgressAllowEntry{
	{"internal/plugins/process", "the process plugin driver, the one place a plugin subprocess is started"},
}

// EgressExecNotYetMigrated is what the tree holds today outside that set.
var EgressExecNotYetMigrated = []EgressAllowEntry{
	{"cmd/cascade", "the composition root runs the operator's own configured commands"},
	{"cmd/cascade/config", "opens the operator's editor"},
	{"internal/build", "the gates shell out to the toolchain to inspect the tree"},
	{"internal/context", "runs the tree-walking helpers the context builder uses"},
	{"internal/daemon", "starts and stops the daemon process"},
	{"internal/daemon/service", "installs the platform service definition"},
	{"internal/doctor", "probes the toolchain the operator has installed"},
	{"internal/secrets", "reads a custody backend through its platform command-line tool"},
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
