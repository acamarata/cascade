// Purpose: cascade-side code-signature checks for the darwin elevation
//
//	helper (R12 F-3): cascade uses the sibling cascade-elevate-helper only
//	when it is Developer ID signed by the SAME team as cascade itself, and
//	only after the code actually running as the child (not just the file on
//	disk) passes the same requirement.
//
// Inputs: this process (SelfTeam, by pid), the helper path
//
//	(VerifyPath), the running child's pid (VerifyPID).
//
// Outputs: sigVerifier, the seam the keystore depends on, and
//
//	codesignVerifier, its production implementation (execs ONLY
//	/usr/bin/codesign by absolute path, empty environment, bounded time and
//	output).
//
// Constraints: no team ID literal lives in any tracked file; the team is
//
//	read from cascade's own signature at run time and validated against
//	[A-Z0-9]{10} before it is placed in a requirement string, so it can never
//	inject requirement syntax. The requirement pins the Developer ID
//	intermediate (…6.2.6) and the Developer ID Application leaf (…6.1.13), so
//	an Apple Development certificate of the same team does not pass, and the
//	signing identifier, so another binary of the same team does not pass.
//
// SPORT: internal/elevation helper signature check/ADDED (P1-SHIP-15).

package elevation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	codesignPath      = "/usr/bin/codesign"
	codesignTimeout   = 5 * time.Second
	codesignMaxOutput = 64 << 10
)

var teamIDPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// sigVerifier is the mutual-signature seam. SelfTeam names the team that
// signed cascade itself; VerifyPath is the cheap pre-filter on the helper
// file; VerifyPID validates the code actually running as the child.
type sigVerifier interface {
	SelfTeam() (string, error)
	VerifyPath(path, team string) error
	VerifyPID(pid int, team string) error
}

// codesignRunner runs /usr/bin/codesign with args and returns its combined
// output. Tests inject a fake; production uses execCodesign.
type codesignRunner func(ctx context.Context, args ...string) ([]byte, error)

type codesignVerifier struct {
	pid func() int // this process: its running image is what signed it, not a path that may have been replaced
	run codesignRunner
}

func newCodesignVerifier() codesignVerifier {
	return codesignVerifier{pid: os.Getpid, run: execCodesign}
}

// execCodesign runs the system codesign with an empty environment and caps
// its output.
func execCodesign(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, codesignTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, codesignPath, args...)
	cmd.Env = []string{}
	cmd.Dir = "/" // a bare all-digit argument is a pid; keep a stray file named like one out of reach
	var out limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	return codesignResult(&out, cmd.Run())
}

// codesignResult turns the captured output and the run error into the
// runner's answer; output that did not fit is an error, never a partial read.
func codesignResult(out *limitedBuffer, err error) ([]byte, error) {
	if err == nil && out.truncated {
		err = errors.New("codesign output exceeded the size limit")
	}
	return out.Bytes(), err
}

// limitedBuffer keeps at most codesignMaxOutput bytes and drops the rest,
// remembering that it did so: a truncated answer is never trusted.
type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := codesignMaxOutput - b.buf.Len()
	if room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	if len(p) > max(room, 0) {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// HelperIdentifier and CallerIdentifier are the code-signing identifiers the
// release signing step must give the helper and the cascade binary
// (codesign -i); the requirements below pin them so another Developer ID
// binary of the same team cannot stand in for either.
const (
	HelperIdentifier = "cascade-elevate-helper"
	CallerIdentifier = "cascade"
)

// HelperRequirement is the code requirement the helper must satisfy for team
// (checked by cascade).
func HelperRequirement(team string) (string, error) { return teamRequirement(team, HelperIdentifier) }

// CallerRequirement is the code requirement a caller of the helper must
// satisfy for team (checked by the helper).
func CallerRequirement(team string) (string, error) { return teamRequirement(team, CallerIdentifier) }

func teamRequirement(team, identifier string) (string, error) {
	if !teamIDPattern.MatchString(team) {
		return "", cascade.New(cascade.KindIntegrity, "elevation: invalid signing team identifier")
	}
	req := `anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6]` +
		` and certificate leaf[field.1.2.840.113635.100.6.1.13]` +
		` and certificate leaf[subject.OU] = "` + team + `"` +
		` and identifier "` + identifier + `"`
	// No injection or debugging entitlement: part of the requirement, so it is
	// judged on the RUNNING image together with the signature (reading the
	// entitlements from the file would not be).
	for _, e := range unsafeEntitlements {
		req += ` and !(entitlement["` + e + `"] exists)`
	}
	return req, nil
}

// parseTeamIdentifier extracts the team from `codesign -dvvv`
// output. Unsigned, ad-hoc and "not set" signatures are errors.
func parseTeamIdentifier(out string) (string, error) {
	team := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "Signature=adhoc":
			return "", cascade.New(cascade.KindIntegrity, "elevation: cascade is ad-hoc signed")
		case strings.HasPrefix(line, "TeamIdentifier="):
			team = strings.TrimPrefix(line, "TeamIdentifier=")
		}
	}
	if !teamIDPattern.MatchString(team) {
		return "", cascade.New(cascade.KindIntegrity, "elevation: cascade has no Developer ID team identifier")
	}
	return team, nil
}

// SelfTeam reads the team from the signature of the RUNNING cascade (by pid),
// not from the file at os.Executable. `codesign -d` on a pid reads the file
// at the process's path, which a same-user process could have replaced, so
// the display comes first and the verification last: it is pinned to the code
// directory hash the display showed, and only the running image can match it.
func (v codesignVerifier) SelfTeam() (string, error) {
	self := strconv.Itoa(v.pid())
	out, err := v.run(context.Background(), "-dvvv", self)
	if err != nil {
		return "", cascade.Wrap(cascade.KindIntegrity, err, "elevation: cascade is not code signed")
	}
	team, err := parseTeamIdentifier(string(out))
	if err != nil {
		return "", err
	}
	cdhash, ok := parseCDHash(string(out))
	if !ok {
		return "", cascade.New(cascade.KindIntegrity, "elevation: cascade has no code directory hash")
	}
	if _, err := v.run(context.Background(), "--verify", "--strict", "-R", `=cdhash H"`+cdhash+`"`, self); err != nil {
		return "", cascade.Wrap(cascade.KindIntegrity, err, "elevation: cascade's own signature does not match the running image")
	}
	return team, nil
}

func (v codesignVerifier) VerifyPath(path, team string) error {
	return v.verify(path, team, false)
}

func (v codesignVerifier) VerifyPID(pid int, team string) error {
	if pid <= 0 {
		return cascade.New(cascade.KindInvalidInput, "elevation: invalid helper pid")
	}
	return v.verify(strconv.Itoa(pid), team, true)
}

// verify reads the helper's hardened-runtime flag and code directory hash,
// then verifies the requirement (team, identifier, no injection entitlement)
// pinned to that hash LAST. For a
// pid the display reads the file at the process's path; a swapped file has a
// different hash, so the final by-pid verification (which judges the running
// image) refuses it.
func (v codesignVerifier) verify(target, team string, isPID bool) error {
	req, err := HelperRequirement(team)
	if err != nil {
		return err
	}
	fail := func(cause error, what string) error {
		return cascade.Wrap(cascade.KindIntegrity, cause, fmt.Sprintf("elevation: helper %s (%s)", what, targetKind(isPID)))
	}
	ctx := context.Background()
	// The hardened-runtime flag and the code hash come from one display of
	// the code directory; the verification below is pinned to that hash.
	out, err := v.run(ctx, "-dvvv", target)
	if err != nil || !hardenedRuntime(string(out)) {
		return fail(err, "lacks the hardened runtime")
	}
	cdhash, ok := parseCDHash(string(out))
	if !ok {
		return fail(nil, "has no code directory hash")
	}
	if _, err := v.run(ctx, "--verify", "--strict", "-R", "="+req+` and cdhash H"`+cdhash+`"`, target); err != nil {
		return fail(err, "is not Developer ID signed by cascade's team")
	}
	return nil
}

var cdHashPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// parseCDHash returns the code directory hash `codesign -dvvv` prints. codesign
// prints the executable path first, and a path may contain newlines, so lines
// forged inside the path precede the real fields: the LAST match wins.
func parseCDHash(out string) (string, bool) {
	hash, found := "", false
	for _, line := range strings.Split(out, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "CDHash="); ok && cdHashPattern.MatchString(h) {
			hash, found = h, true
		}
	}
	return hash, found
}

// unsafeEntitlements let code be loaded into, or attached to, a signed image.
var unsafeEntitlements = []string{
	"com.apple.security.cs.allow-dyld-environment-variables",
	"com.apple.security.cs.disable-library-validation",
	"com.apple.security.get-task-allow",
	"com.apple.security.cs.debugger",
}

// hardenedRuntime reports whether `codesign -dvvv` output shows the runtime
// flag on the code directory. As with the hash, the LAST code directory line
// is the real one (a forged line can only sit inside the executable path,
// which is printed first).
func hardenedRuntime(out string) bool {
	runtimeFlag := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CodeDirectory v=") {
			if i := strings.Index(line, "flags="); i >= 0 {
				runtimeFlag = strings.Contains(line[i:], "runtime")
			}
		}
	}
	return runtimeFlag
}

func targetKind(isPID bool) string {
	if isPID {
		return "running process"
	}
	return "file"
}
