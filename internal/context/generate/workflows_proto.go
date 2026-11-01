package generate

// Purpose: the harness workflows protocol (contract:workflows-probe). A
//   WorkflowsSupportProbe reports what the installed harness CLI offers;
//   Negotiate maps that report onto protocol revision 1 and refuses every
//   other outcome with the one sentinel ErrHarnessIncompatible.
// Inputs: a probe; the --version and --help output it captured.
// Outputs: a ProbeResult with a per-process Seq, and nil or
//   ErrHarnessIncompatible.
// Constraints: fail closed. Only a parsed Commands: section that lists
//   `workflows` as a command, together with a version at or above
//   MinCLIVersion, is supported. A mention in prose, an unknown result or
//   an unparsable version is never supported. Every call re-probes.
// SPORT: context-engine/workflows-probe (ADD, P1-GEN-09).

import (
	"bufio"
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The protocol revisions this package speaks: revision 1 only.
const (
	// ProtocolMin is the oldest protocol revision this package speaks.
	ProtocolMin = 1
	// ProtocolMax is the newest protocol revision this package speaks.
	ProtocolMax = 1
)

// MinCLIVersion is the oldest harness CLI version revision 1 accepts.
const MinCLIVersion = "2.1.273"

// The four probe results.
const (
	resultSupported     = "supported"
	resultUnsupported   = "unsupported"
	resultIndeterminate = "indeterminate"
	resultOutOfRange    = "out-of-range"
)

// workflowsCommand is the command name the --help Commands: section must list.
const workflowsCommand = "workflows"

// ErrHarnessIncompatible is the one refusal Negotiate returns for every
// outcome but revision 1. Compare it by identity, never with errors.Is
// (which compares Kind only).
var ErrHarnessIncompatible = cascade.New(cascade.KindUnsupported, "generate: harness workflows surface incompatible")

// ProbeResult is one probe of the harness CLI. Result is one of the four
// Result* constants; Seq is set by Negotiate.
type ProbeResult struct {
	Result          string
	Binary, Version string
	ExitCode        int
	Seq             int
}

// WorkflowsSupportProbe reports what the installed harness CLI offers.
type WorkflowsSupportProbe interface {
	Probe(ctx context.Context) (ProbeResult, error)
}

// probeKey is what makes two probe results the same record.
type probeKey struct {
	binary, version string
	exit            int
}

// probeLedger holds the current recorded probe result of this process.
type probeLedger struct {
	mu      sync.Mutex
	seq     int
	has     bool
	key     probeKey
	current ProbeResult
}

// ledger is the process-wide record Negotiate de-duplicates against.
var ledger = &probeLedger{}

// record stores r: the same (Binary, Version, ExitCode) as the current
// record keeps its Seq; anything else replaces it under the next Seq.
func (l *probeLedger) record(r ProbeResult) ProbeResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := probeKey{r.Binary, r.Version, r.ExitCode}
	if l.has && l.key == k && l.current.Result == r.Result {
		return l.current
	}
	l.seq++
	r.Seq = l.seq
	l.has, l.key, l.current = true, k, r
	return r
}

// Negotiate probes the harness again and records the result. Revision 1
// returns (result, nil); every other outcome returns (result,
// ErrHarnessIncompatible). A probe failure is returned as is.
func Negotiate(ctx context.Context, p WorkflowsSupportProbe) (ProbeResult, error) {
	if p == nil {
		return ProbeResult{}, cascade.New(cascade.KindInvalidInput, "generate: nil workflows probe")
	}
	r, err := p.Probe(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	if !knownResult(r.Result) {
		r.Result = resultIndeterminate
	}
	r = ledger.record(r)
	rev := revisionFor(r.Result)
	if rev < ProtocolMin || rev > ProtocolMax {
		return r, ErrHarnessIncompatible
	}
	return r, nil
}

// knownResult reports whether s is one of the four results.
func knownResult(s string) bool {
	switch s {
	case resultSupported, resultUnsupported, resultIndeterminate, resultOutOfRange:
		return true
	}
	return false
}

// revisionFor maps a result onto a protocol revision; 0 is none.
func revisionFor(result string) int {
	if result == resultSupported {
		return 1
	}
	return 0
}

// classify decides the result from the two captured outputs. A failed
// invocation, an unparsable version or a --help without a Commands:
// section is indeterminate.
func classify(versionOut []byte, versionExit int, helpOut []byte, helpExit int) (string, string) {
	version, ok := parseVersion(versionOut)
	if versionExit != 0 || !ok {
		return resultIndeterminate, version
	}
	cmds, found := helpCommands(helpOut)
	switch {
	case helpExit != 0 || !found:
		return resultIndeterminate, version
	case !cmds[workflowsCommand]:
		return resultUnsupported, version
	case compareVersions(version, MinCLIVersion) < 0:
		return resultOutOfRange, version
	}
	return resultSupported, version
}

// versionShape is the leading semantic version of `--version` output.
var versionShape = regexp.MustCompile(`^(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:\s|$)`)

// parseVersion reads "X.Y.Z" from the first line of out. It returns the
// trimmed first line (bounded) and false when no version leads it.
func parseVersion(out []byte) (string, bool) {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	line = strings.TrimSpace(line)
	m := versionShape.FindStringSubmatch(line)
	if m == nil {
		if len(line) > 64 {
			line = line[:64]
		}
		return line, false
	}
	return m[1] + "." + m[2] + "." + m[3], true
}

// compareVersions orders two parsed X.Y.Z versions numerically.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// helpCommands parses the Commands: section of --help output. A command
// line is indented exactly two spaces; its first token, split on "|",
// names the command and its aliases. Deeper-indented lines continue a
// description and name nothing. found is false without a Commands: line.
func helpCommands(out []byte) (map[string]bool, bool) {
	cmds := map[string]bool{}
	found := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if !found {
			found = line == "Commands:"
			continue
		}
		if line == "" || !strings.HasPrefix(line, " ") {
			break
		}
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		for _, alias := range strings.Split(name, "|") {
			cmds[alias] = true
		}
	}
	return cmds, found
}
