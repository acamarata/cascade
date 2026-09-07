// Package census implements the fleet process census (P1-E12-W3-S24-T1):
// a single-shot, build-tagged, cross-platform scan for live claude, codex
// and opencode processes, with each discovered process attributed to a
// Cascade account.
//
// Purpose: enumerate matching processes and reduce each one to Snapshot,
//
//	the R-21.271 first-boundary-redacted shape — never a raw argv or
//	environment read.
//
// Inputs: an account-directory map (known Cascade account directory path
//
//	-> account alias) supplied by the caller of New; platform enumeration
//	takes no other input and performs no file I/O beyond the platform
//	read itself.
//
// Outputs: Enumerator.Enumerate returns []Snapshot or a pkg/cascade
//
//	taxonomy error. A platform enumeration failure is a typed error, never
//	a silently empty result; an unresolvable account attribution is an
//	empty Snapshot.Account, never an error.
//
// Constraints: R-21.271 (extending R-21.152) — Snapshot carries exactly
//
//	Pid, Binary (basename only), Account (alias only) and an allowlisted
//	Flags subset. There is no argv field and no environment field on
//	Snapshot; the raw platform read (rawProcess) never leaves this
//	package and is discarded inside Enumerate, before any value returns
//	to a caller. This ticket extends R-21.271 to EVERY platform, not only
//	linux: no platform backend in this package reads process environment,
//	ever — attribution is argv-only on darwin, linux and (moot, since it
//	always refuses) windows alike. Polling/streaming is T3's
//	responsibility; this package exposes a single-shot Enumerate only.
//
// CONTRACT DEVIATION (naming, recorded, not papered over). The contract's
// task list names "Census interface" and a "CensusUnsupported error type".
// Both would violate revive's exported-name stutter check in a package
// named census (census.Census, census.CensusUnsupported) — the exact class
// of defect AGENT-BRIEF.md calls out by name ("a stuttering exported name
// like scope.ScopeKind"). This file instead names the interface Enumerator
// (mirrors io.Reader's naming, since Enumerate is this type's one method)
// and follows internal/fleet/governor.ErrUnsupportedPlatform's already-
// landed sentinel-error precedent instead of a bespoke error type — both
// changes are naming-only; every behavior the contract describes is
// unchanged.
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).
package census

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// unsupportedPlatformMsg is asserted verbatim by census_windows_test.go's
// build-tagged case, mirroring providers/sqlite/flock_windows.go's
// windowsLockRefusalMsg precedent — keep the two in sync if this message
// ever changes.
const unsupportedPlatformMsg = "census: process enumeration is not supported on this platform (tier-2 scope)"

// ErrUnsupportedPlatform is the sentinel a platform backend returns when it
// has no real enumeration implementation (windows, tier-2 per 06-FORGE-SPEC
// §2). Callers must treat it as a refusal, not a transient failure: retrying
// will not help.
var ErrUnsupportedPlatform = cascade.New(cascade.KindUnsupported, unsupportedPlatformMsg)

// trackedBinaries is the closed set of process basenames this census
// reports on. A process whose argv[0] basename is not in this set is never
// included in a Snapshot.
var trackedBinaries = map[string]bool{
	"claude":   true,
	"codex":    true,
	"opencode": true,
}

// allowedFlags is the R-21.271 flag allowlist: value-free flag names that
// may appear on a Snapshot's Flags. This is an ALLOWLIST, not a denylist
// (AGENT-BRIEF.md's redaction instruction) — a flag not named here is
// dropped by default, including one nobody anticipated, rather than
// leaking until someone remembers to deny it.
var allowedFlags = map[string]bool{
	"--version":                      true,
	"--help":                         true,
	"--debug":                        true,
	"--verbose":                      true,
	"--json":                         true,
	"--continue":                     true,
	"--resume":                       true,
	"--print":                        true,
	"--yolo":                         true,
	"--dangerously-skip-permissions": true,
}

// Snapshot is one discovered process, redacted at the first boundary
// (R-21.271). It carries ONLY this closed field set: there is structurally
// no field for a raw argv or environment value, so a caller cannot leak
// what it was never handed.
type Snapshot struct {
	// Pid is the process id.
	Pid int `json:"pid"`
	// Binary is the process's binary basename only, never its full path.
	Binary string `json:"binary"`
	// Account is the matched Cascade account alias, or "" when
	// attribution could not resolve one account unambiguously.
	Account string `json:"account"`
	// Flags is an allowlisted, value-free subset of the process's flags.
	Flags []string `json:"flags"`
}

// snapshotFieldPin is a compile-time assertion that Snapshot's field set
// has not silently drifted (R-21.271's "no full argv field" requirement).
// A struct conversion in Go requires identical field names, types and
// order (tags aside), so adding, removing, renaming or reordering a
// Snapshot field breaks this line at compile time rather than at review
// time.
var snapshotFieldPin = struct {
	Pid     int
	Binary  string
	Account string
	Flags   []string
}(Snapshot{})

// rawProcess is the raw platform read: a pid and its argv tokens. It never
// leaves this package's internals and is discarded inside Enumerate, in
// the same call that produces the Snapshot slice returned to callers.
type rawProcess struct {
	pid  int
	argv []string
}

// procSource matches each platform backend's enumerateRaw signature, and
// is the seam Enumerator injects for testing: census_test.go drives
// Enumerate with a fake source instead of a live process table, so the
// cross-platform logic (tracked-binary filtering, attribution, flag
// redaction) is fully testable without touching syscalls on any platform.
type procSource func() ([]rawProcess, error)

// Enumerator is the fleet process census's public surface. The zero value
// of its implementation is not usable; construct with New.
type Enumerator interface {
	// Enumerate performs one single-shot process scan and returns the
	// redacted Snapshot for every tracked-binary process found. A
	// platform enumeration failure returns a pkg/cascade taxonomy error;
	// an unresolvable account attribution is never an error.
	Enumerate() ([]Snapshot, error)
}

// census is Enumerator's concrete implementation.
type census struct {
	source      procSource
	accountDirs map[string]string
}

// New builds an Enumerator backed by the current platform's real
// enumeration backend. accountDirs maps a known Cascade account directory
// path to that account's alias; a nil or empty map means every process
// attributes to "" (unknown).
func New(accountDirs map[string]string) Enumerator {
	return &census{source: enumerateRaw, accountDirs: accountDirs}
}

// Enumerate implements Enumerator.
func (c *census) Enumerate() ([]Snapshot, error) {
	raws, err := c.source()
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, rp := range raws {
		if len(rp.argv) == 0 || !isTrackedBinary(rp.argv[0]) {
			continue
		}
		out = append(out, buildSnapshot(rp, c.accountDirs))
	}
	return out, nil
}

// isTrackedBinary reports whether argv0's basename is one this census
// reports on.
func isTrackedBinary(argv0 string) bool {
	return trackedBinaries[filepath.Base(argv0)]
}

// buildSnapshot reduces one raw platform read to its redacted Snapshot.
// This is where rp's argv is consumed for the last time inside this
// package; nothing downstream of this call retains it.
func buildSnapshot(rp rawProcess, accountDirs map[string]string) Snapshot {
	return Snapshot{
		Pid:     rp.pid,
		Binary:  filepath.Base(rp.argv[0]),
		Account: attributeAccount(rp.argv, accountDirs),
		Flags:   redactFlags(rp.argv),
	}
}

// redactFlags returns the allowlisted, value-free subset of argv's flags
// (argv[0], the binary path, is never itself considered a flag). Anything
// not an exact allowlist match — an unknown flag, a flag operand, or a
// positional argument — is dropped, fail-closed per the allowlist rule.
func redactFlags(argv []string) []string {
	var flags []string
	for _, tok := range argv[1:] {
		if allowedFlags[tok] {
			flags = append(flags, tok)
		}
	}
	return flags
}

// parseLinuxCmdline splits a /proc/<pid>/cmdline NUL-separated buffer into
// its argv tokens. It is untagged, alongside internal/fleet/governor's
// parseProcStat/parseProcMeminfo precedent, so it compiles and can be
// fuzzed on every host platform, not only linux. A trailing NUL (the
// common case) yields no trailing empty token; an empty buffer yields nil.
func parseLinuxCmdline(data []byte) []string {
	trimmed := strings.TrimRight(string(data), "\x00")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\x00")
}

// parseDarwinProcArgs2 decodes a kern.procargs2 sysctl buffer into its
// argv tokens only, per the CONTRACT DEVIATION recorded in
// census_darwin.go: the buffer also contains the process's environment,
// which this function never parses or returns — env bytes are simply
// never walked past argc tokens, so they are discarded with the rest of
// buf when the caller's stack frame returns. Untagged for the same
// fuzz-on-every-host reason as parseLinuxCmdline.
func parseDarwinProcArgs2(buf []byte) ([]string, error) {
	if len(buf) < 4 {
		return nil, cascade.New(cascade.KindInvalidInput, "census: procargs2 buffer too short")
	}
	argc := int(buf[0]) | int(buf[1])<<8 | int(buf[2])<<16 | int(buf[3])<<24
	rest := buf[4:]

	execEnd := bytes.IndexByte(rest, 0)
	if execEnd < 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "census: procargs2 has no exec-path terminator")
	}
	rest = rest[execEnd:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}

	argv := make([]string, 0, argc)
	for i := 0; i < argc && len(rest) > 0; i++ {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			argv = append(argv, string(rest))
			break
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return argv, nil
}
