package opencode

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Purpose: the opencode-binary detect predicate. Auto-enables cascade-opencode
//   when the opencode CLI is reachable in PATH; dormant, without error, when
//   it is not.
// Inputs: the process's PATH (via the injectable lookPath variable).
// Outputs: a bool; Detect itself never errors — an unresolvable PATH reads
//   as "not present".
// Constraints: os/exec is denied to plugins/** by the A-T2 process-spawn
//   allowlist (internal/build/egress_allow.go's EgressExecNormative names
//   only internal/plugins/process, "the one place a plugin subprocess is
//   started" - a PATH existence probe is not a spawn, but importing
//   os/exec at all trips the gate regardless of which symbol is used).
//   defaultLookPath below is therefore a small, real reimplementation of
//   exec.LookPath's PATH-search semantics using only os/path/filepath/
//   runtime/strings, none of which are gated. lookPath is a package
//   variable, not a hard-coded call, so detect_test.go controls both the
//   present and absent states without touching the real filesystem.
// SPORT: plugins/opencode detect (ADD) — P1-E16-W4-S34-T3.

// lookPath resolves a binary name to its full path, following PATH. It
// defaults to defaultLookPath and is swapped by detect_test.go to exercise
// both the present and absent states deterministically.
var lookPath = defaultLookPath

// opencodeBinary is the executable name Detect probes for.
const opencodeBinary = "opencode"

// defaultLookPath searches PATH for an executable file named name, the
// same semantics exec.LookPath provides, without importing os/exec (see
// the package doc comment for why). On Windows it also tries each
// PATHEXT suffix, matching exec.LookPath's own Windows behavior; on other
// platforms a candidate must have at least one execute bit set.
func defaultLookPath(name string) (string, error) {
	notFound := fmt.Errorf("%s: executable file not found in $PATH", name)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, cand := range candidateNames(name) {
			path := filepath.Join(dir, cand)
			if isExecutableFile(path) {
				return path, nil
			}
		}
	}
	return "", notFound
}

// candidateNames returns the file names to probe for name in one PATH
// directory: name itself on non-Windows, or name plus every PATHEXT
// suffix on Windows.
func candidateNames(name string) []string {
	if runtime.GOOS != "windows" {
		return []string{name}
	}
	exts := strings.Split(os.Getenv("PATHEXT"), string(filepath.ListSeparator))
	out := make([]string, 0, len(exts)+1)
	for _, ext := range exts {
		if ext != "" {
			out = append(out, name+ext)
		}
	}
	return append(out, name)
}

// isExecutableFile reports whether path names a regular file with at
// least one execute bit set (non-Windows), or simply exists as a regular
// file (Windows, where the candidate name already carries a PATHEXT
// suffix and execute bits are not POSIX-meaningful).
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

// Detect reports whether the opencode binary is reachable in PATH. It never
// returns an error: an unresolvable binary is a legitimate, expected
// "not installed" state, not a failure — the plugin host never errors on a
// detect=false builtin.
func Detect() bool {
	_, err := lookPath(opencodeBinary)
	return err == nil
}

// runDetect is RunCommand's "detect" handler. Success is silent (nil) in
// both the present and absent cases, matching plugins/pbd's own
// RunCommand idiom (plugins/pbd/validate.go's runValidateAndSummarize):
// this package, unlike plugins/examples (the one path the repo-wide
// output gate exempts, internal/build/outputgate.go), may not write to
// stdout directly, and detect finding nothing is not a failure to report
// through the error channel either - a caller that needs the boolean
// calls Detect() itself.
func runDetect() error {
	Detect()
	return nil
}
