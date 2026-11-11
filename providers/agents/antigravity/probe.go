package antigravity

// Purpose: classify the installed print interface against revision 1.
// Inputs: injected executable lookup and help capture.
// Outputs: a typed present, absent or changed result.
// Constraints: no credentials, listeners or implicit child environment.
// SPORT: providers.agents.antigravity/ADD (P1-AGT-07c).

import (
	"context"
	_ "embed"
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// ProbeOutcome is the observed availability of the print interface.
type ProbeOutcome string

const (
	// ProbePresent means every recorded help token is supported.
	ProbePresent ProbeOutcome = "present"
	// ProbeAbsent means agy could not be found on the executable path.
	ProbeAbsent ProbeOutcome = "absent"
	// ProbeChanged means lookup or help could not establish the contract.
	ProbeChanged ProbeOutcome = "changed"
)

// ProbeResult reports the observed version and the reason for refusal.
type ProbeResult struct {
	Outcome ProbeOutcome
	Version string
	Reason  string
}

//go:embed testdata/protocol/1/help-tokens.txt
var requiredHelpTokens string

var helpVersion = regexp.MustCompile(`(?im)^\s*(?:agy|antigravity)(?:\s+version)?\s+v?([0-9]+\.[0-9]+\.[0-9]+)\s*$`)
var helpOption = regexp.MustCompile(`^(?:-[A-Za-z0-9],?\s+)?(--[a-z][a-z0-9-]*)(?:[ =]|$)`)

// Probe checks the revision-1 help contract using the production parser.
// Only exec.ErrNotFound is absence; other lookup errors fail as changed.
// Version drift is reported here and refused separately by CheckHarness.
func Probe(ctx context.Context, lookPath func(string) (string, error), runHelp func(context.Context, string) ([]byte, error)) ProbeResult {
	if lookPath == nil || runHelp == nil {
		return ProbeResult{Outcome: ProbeChanged, Reason: "probe dependencies are missing"}
	}
	if err := ctx.Err(); err != nil {
		return ProbeResult{Outcome: ProbeChanged, Reason: "probe context canceled"}
	}
	bin, err := lookPath("agy")
	if errors.Is(err, exec.ErrNotFound) {
		return ProbeResult{Outcome: ProbeAbsent, Reason: "agy executable not found"}
	}
	if err != nil || bin == "" {
		return ProbeResult{Outcome: ProbeChanged, Reason: "agy executable lookup failed"}
	}
	help, err := runHelp(ctx, bin)
	if err != nil || ctx.Err() != nil {
		return ProbeResult{Outcome: ProbeChanged, Reason: "agy help capture failed"}
	}
	result := ProbeResult{Outcome: ProbePresent}
	if version := helpVersion.FindSubmatch(help); len(version) == 2 {
		result.Version = string(version[1])
	}
	flags := helpFlags(string(help))
	for _, token := range strings.Fields(requiredHelpTokens) {
		if !flags[token] {
			result.Outcome = ProbeChanged
			result.Reason = "protocol " + string(ProtocolRevision) + " help missing " + token
			break
		}
	}
	return result
}

// helpFlags reads declarations at the start of option lines, never flags
// quoted in a different option's description. Format choices must belong
// to --output-format, including its indented continuation lines.
func helpFlags(help string) map[string]bool {
	flags := make(map[string]bool)
	formatIndent := -1
	for _, line := range strings.Split(help, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)
		match := helpOption.FindStringSubmatch(trimmed)
		if len(match) == 2 {
			flags[match[1]] = true
			formatIndent = -1
			if match[1] == "--output-format" {
				formatIndent = indent
			}
		} else if trimmed != "" && indent <= formatIndent {
			formatIndent = -1
		}
		if formatIndent >= 0 {
			for _, word := range strings.FieldsFunc(trimmed, func(r rune) bool {
				return (r < 'a' || r > 'z') && r != '-'
			}) {
				if word == "stream-json" {
					flags[word] = true
				}
			}
		}
	}
	return flags
}
