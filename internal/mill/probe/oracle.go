// Package probe provides oracle-based checks that run in code after every step.
package probe

import (
	"fmt"
	"strings"
)

// Step represents a replayable action taken during probing.
type Step struct {
	// Action is the type of action performed.
	Action string
	// Target is the element or resource the action was performed on.
	Target string
}

// Finding holds the result of an oracle check.
type Finding struct {
	// Oracle is the name of the oracle that produced this finding.
	Oracle string
	// Detail describes the issue found.
	Detail string
	// Steps is the list of steps leading to this finding.
	Steps []Step
}

// State represents the current application state for oracle evaluation.
type State struct {
	// ConsoleErrors contains any console error messages.
	ConsoleErrors []string
	// NetworkResponses maps URL patterns to HTTP status codes.
	NetworkResponses map[string]int
	// OverflowElements lists elements that have layout overflow.
	OverflowElements []string
	// A11yViolations lists accessibility violation identifiers.
	A11yViolations []string
	// Steps records the actions taken so far.
	Steps []Step
}

// Oracle is the interface that all code-based checks implement.
type Oracle interface {
	// Name returns the identifier for this oracle.
	Name() string
	// Check evaluates the current state and returns any findings.
	Check(state State) []Finding
}

// ConsoleErrorOracle detects console errors in the application state.
type ConsoleErrorOracle struct{}

// Name returns "console-error".
func (o ConsoleErrorOracle) Name() string { return "console-error" }

// Check returns a finding for each console error present in the state.
func (o ConsoleErrorOracle) Check(state State) []Finding {
	var findings []Finding
	for _, err := range state.ConsoleErrors {
		findings = append(findings, Finding{
			Oracle: o.Name(),
			Detail: fmt.Sprintf("console error: %s", err),
			Steps:  state.Steps,
		})
	}
	return findings
}

// AccessibilityOracle detects accessibility violations in the application state.
type AccessibilityOracle struct{}

// Name returns "accessibility".
func (o AccessibilityOracle) Name() string { return "accessibility" }

// Check returns a finding for each accessibility violation present in the state.
func (o AccessibilityOracle) Check(state State) []Finding {
	var findings []Finding
	for _, v := range state.A11yViolations {
		findings = append(findings, Finding{
			Oracle: o.Name(),
			Detail: fmt.Sprintf("a11y violation: %s", v),
			Steps:  state.Steps,
		})
	}
	return findings
}

// NetworkErrorOracle flags HTTP 4xx and 5xx responses.
type NetworkErrorOracle struct{}

// Name returns "network-error".
func (o NetworkErrorOracle) Name() string { return "network-error" }

// Check returns a finding for each network response with a 4xx or 5xx status code.
func (o NetworkErrorOracle) Check(state State) []Finding {
	var findings []Finding
	for url, code := range state.NetworkResponses {
		if code >= 400 && code < 600 {
			findings = append(findings, Finding{
				Oracle: o.Name(),
				Detail: fmt.Sprintf("HTTP %d on %s", code, url),
				Steps:  state.Steps,
			})
		}
	}
	return findings
}

// LayoutOverflowOracle detects layout overflow issues.
type LayoutOverflowOracle struct{}

// Name returns "layout-overflow".
func (o LayoutOverflowOracle) Name() string { return "layout-overflow" }

// Check returns a finding for each element that has layout overflow.
func (o LayoutOverflowOracle) Check(state State) []Finding {
	var findings []Finding
	for _, el := range state.OverflowElements {
		findings = append(findings, Finding{
			Oracle: o.Name(),
			Detail: fmt.Sprintf("overflow on %s", el),
			Steps:  state.Steps,
		})
	}
	return findings
}

// RunOracles executes all provided oracles against the given state and aggregates findings.
// Oracles run purely in code with no external dependencies.
func RunOracles(oracles []Oracle, state State) []Finding {
	var all []Finding
	for _, o := range oracles {
		all = append(all, o.Check(state)...)
	}
	return all
}

// ensure strings import is used
var _ = strings.TrimSpace
