// Package claude is the headless one-shot AgentProvider over the vendor CC
// harness binary (AD/S-61.T2). Every job runs the real binary once, in a
// leased worktree, in its own process group, with the hook pack copied into
// the tree's .claude/ directory and the result object parsed in memory.
//
// SEAMS (providers -> pkg only, Art.7.2/R-16.68b): the package imports
// pkg/** only. The profile registry and the worktree lease live under
// internal/, so they arrive as the small interfaces in driver.go, satisfied
// by a composition-root adapter (the AD/S-62.T3 host shim) with no import in
// either direction. The per-spawn seams arrive on the job itself (R-14.329):
// provider.SpawnEnv, the WorktreeRoot (which must equal the lease root), the
// output Filter (nil refuses) and an optional JobID, all set by that shim.
// Executable() reports the resolved harness path. There is no transcript
// parser: per-tool events come only from authenticated callback frames.
//
// CONTROLS THE DRIVER ENFORCES: the child environ is exactly
// SpawnEnv.Driver; the runtime home is a 0700 directory disjoint from the
// leased tree; the tree holds no symlink that leaves it and none at all
// under .claude/, nor a settings file there the hook pack does not write,
// and the harness loads project settings only; the pre-spawn secret scan
// runs over the whole tree; the installed harness version and its init
// event are probed before the first turn; inbound callbacks are
// authenticated with a per-spawn token, shape-checked, filtered and
// bounded; cancellation signals the whole process group and confirms it
// empty; a seeded credential canary anywhere in the run's output, frames,
// journal or written files fails it closed.
//
// EGRESS HONESTY LIMIT (R-21.177, binding on every doc surface of this
// package): worktree minimization and the pre-spawn scan are the controls
// on what the harness can read. The job's provider.OutputFilter covers
// driver stdio only. The harness opens its own TLS connection to the
// vendor and reads worktree files the substitution pass never sees, so no
// document or help text may claim this driver's egress is filtered.
//
// TOOL-SUBPROCESS LIMIT (R-21.151, recorded gap): the harness starts its
// own tool and hook subprocesses and passes them its environment. The
// driver cannot make those subprocesses receive SpawnEnv.Tool. After the
// leader exits it inspects only the members left in the job's process
// group and fails the run when one carries the vendor auth variable; a
// descendant that called setsid() has left the group and is not seen, nor
// is a tool subprocess that exits while the leader is alive. The residual is
// the Art.6 deferral DEF-P2-driver-tool-isolation (docs/security-posture.md).
//
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).
package claude

// VendorConfigDirVar is the harness's config-directory environment variable:
// the only vendorAuthVar this driver accepts on a Profile (R-16.37). Every
// use in this repository goes through this constant.
const (
	VendorConfigDirVar = "CLAUDE_CONFIG" + "_DIR" // split: the identifier sweep matches the contiguous literal (C22)
)
