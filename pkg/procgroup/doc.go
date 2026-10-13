// Package procgroup starts a child process as the leader of its own process
// group and signals that whole group, so a grandchild the child forked
// cannot outlive a kill aimed at the child.
//
// Purpose: the one place in the tree that signals a process group. The
//
//	local CI gate, the nself detection probe, the process-plugin
//	runtime and the claude agent driver all call it instead of each
//	holding its own copy.
//
// Inputs: a pgid (the leader's pid, because Attr makes the child its own
//
//	group leader) and a signal; Lookup takes any pid.
//
// Outputs: Attr's SysProcAttr for the platform; Signal's typed error;
//
//	Lookup's pgid or typed error (a vanished pid is a NoSuchProcessError);
//	the Exists and Empty probes; the Sig type with SigTerm and SigKill.
//
// Constraints: imports syscall and pkg/cascade only, never os/exec, so the
//
//	os/exec importer allowlist (internal/build/egress_allow.go) is
//	unchanged. Signal refuses a pgid of 1 or below: kill(2) reads a
//	target of 0 as the caller's own group and -1 as every process the
//	caller may signal. Windows has no process groups in this sense, so
//	Attr is nil there and Signal refuses with KindUnsupported.
//
// SPORT: pkg/procgroup (ADD) — P1-PLG-09; (CHANGE) — P1-AGT-01.
package procgroup
