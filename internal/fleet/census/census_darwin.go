//go:build darwin

package census

// Purpose: darwin's enumerateRaw — live PID enumeration and per-PID argv
//
//	extraction.
//
// Inputs: none (calls the injected kinfoProcListFn/procArgs2Fn seams,
//
//	which census_darwin_test.go overrides with fakes so tests never touch
//	the live process table).
//
// Outputs: the set of (pid, argv) pairs found; a pid whose argv read
//
//	fails (permission denied, or the process exited mid-scan) is skipped
//	individually, mirroring the linux backend's EACCES-skip contract.
//
// Constraints: no CGO (PRI hard rule 2). CONTRACT DEVIATION: the ticket's
//
//	task text names "proc_listpids + proc_pidinfo via syscall/unix", but
//	golang.org/x/sys/unix v0.47.0 (this module's pinned version, verified
//	by symbol search across the vendored source) exposes neither —
//	libproc's proc_listpids/proc_pidinfo require CGO or a private
//	dlopen shim, and PRI hard rule 2 forbids CGO in core. This backend
//	instead uses two pure-Go sysctl reads that ps(1) and lsof(8) are
//	themselves built on: unix.SysctlKinfoProcSlice("kern.proc.all") for
//	live PIDs (nametomib resolves this name; verified empirically against
//	this build host) and unix.SysctlRaw("kern.procargs2", pid) for each
//	pid's raw argument/environment buffer, decoded by the untagged
//	parseDarwinProcArgs2 in census.go. That buffer contains the process's
//	environment as well as its argv, per the documented kern.procargs2
//	layout; parseDarwinProcArgs2 stops after the declared argc argv
//	tokens and never walks into the environment region, so no
//	environment byte is ever parsed, returned, or retained — matching
//	R-21.271's "never read process environment on any platform" rule by
//	construction rather than by omission. This mirrors
//	internal/fleet/governor/sampler_darwin.go's own recorded sysctl
//	CONTRACT DEVIATION for the identical reason (no Mach traps, no CGO).
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).

import (
	"golang.org/x/sys/unix"

	"github.com/acamarata/cascade/pkg/cascade"
)

// kinfoProcListFn and procArgs2Fn are the injectable syscall seams
// census_darwin_test.go overrides with fakes, so this package's darwin
// tests never depend on the machine's live process table.
var (
	kinfoProcListFn = func() ([]unix.KinfoProc, error) {
		return unix.SysctlKinfoProcSlice("kern.proc.all")
	}
	procArgs2Fn = func(pid int) ([]byte, error) {
		return unix.SysctlRaw("kern.procargs2", pid)
	}
)

// enumerateRaw implements this package's platform seam for darwin.
func enumerateRaw() ([]rawProcess, error) {
	procs, err := kinfoProcListFn()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "census: enumerate darwin processes")
	}
	var out []rawProcess
	for _, p := range procs {
		pid := int(p.Proc.P_pid)
		raw, rerr := procArgs2Fn(pid)
		if rerr != nil {
			continue // permission denied or the process exited mid-scan: skip this pid only
		}
		argv, perr := parseDarwinProcArgs2(raw)
		if perr != nil || len(argv) == 0 {
			continue
		}
		out = append(out, rawProcess{pid: pid, argv: argv})
	}
	return out, nil
}
