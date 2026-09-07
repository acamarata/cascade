//go:build linux

package census

// Purpose: linux's enumerateRaw — iterates /proc for numeric PID
//
//	directories and reads each one's /proc/<pid>/cmdline for argv.
//
// Inputs: none (reads procRoot, "/proc" in production; overridable in
//
//	this package's own linux-tagged tests so they never depend on the
//	live process table).
//
// Outputs: the set of (pid, argv) pairs found; a pid whose cmdline read
//
//	fails (permission denied, or the process exited between the
//	directory listing and the read) is skipped individually rather than
//	aggregated into a whole-census failure, per this ticket's contract.
//	A failure to list procRoot itself IS a whole-census, typed failure.
//
// Constraints: R-21.271 — this backend never reads /proc/<pid>/environ.
//
//	Attribution and flag extraction both work from cmdline tokens only.
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// procRoot is overridden by census_linux_test.go to a synthetic directory
// tree, so tests never depend on the machine's live process table.
var procRoot = "/proc"

// enumerateRaw implements this package's platform seam for linux.
func enumerateRaw() ([]rawProcess, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "census: read /proc")
	}
	var out []rawProcess
	for _, e := range entries {
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil {
			continue // not a PID directory
		}
		argv, rerr := readLinuxCmdline(pid)
		if rerr != nil || len(argv) == 0 {
			continue // EACCES, ENOENT (exited mid-scan), or empty: skip this pid only
		}
		out = append(out, rawProcess{pid: pid, argv: argv})
	}
	return out, nil
}

// readLinuxCmdline reads and parses one pid's cmdline file. The raw bytes
// are consumed by parseLinuxCmdline and go out of scope when this function
// returns; nothing beyond the resulting argv slice is retained.
func readLinuxCmdline(pid int) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	return parseLinuxCmdline(data), nil
}
