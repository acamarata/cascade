// Purpose: AMD-20260916/6 (R-14.249 / R-P2.61) -- the checklist the native
//   adversarial reviewer must ANSWER, and the required checks it must see
//   real execution evidence for (P1-E25-W5-S52-T4, CR fix D3). The
//   checklist rides in provider.ReviewRequest.Context, the ABI's free-text
//   carrier: pkg/provider's frozen ReviewRequest has no checklist field and
//   this ticket may not widen it, so the format is DOCUMENTED (docs/
//   review.md § Checklist) rather than invented per call.
// Inputs: the caller's free-text ReviewRequest.Context, after the R-21.191
//   section filter has run over it.
// Outputs: a Checklist (dimensions to answer, checks that must show an
//   executed command and its exit code), the prompt text that instructs the
//   model, and the findings a response that skipped either produces.
// Constraints: a required check with NO execution record becomes a
//   High-severity "unexecuted check" finding (this package's own
//   ReviewSeverityMajor is the ABI's nearest non-blocking-optional tier; the
//   contract's word "High" maps to it, and the message cites D7). The
//   reviewer never accepts a worker's word that a test ran: "ran clean" with
//   no command and no exit code is exactly the input this refuses.
// SPORT: internal/review.checklist/ADD (P1-E25-W5-S52-T4).

package review

import (
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/provider"
)

// unexecutedCheckTitle is the exact finding title AMD-20260916/6 names.
// Asserted verbatim by TestReviewerRejectsUnexecutedCheck, so a reworded
// message cannot quietly retire the rule.
const unexecutedCheckTitle = "unexecuted check"

// The two documented Context spellings a caller uses to declare a check.
// Both are line-anchored and case-insensitive:
//
//	CHECK: go test -race ./internal/review/...
//
//	checks:
//	  - go test -race ./internal/review/...
//	  - golangci-lint run ./internal/review/...
//
// The fenced form ends at the first line that is neither blank nor a
// `- `/`* ` list item.
var (
	checkLinePattern  = regexp.MustCompile(`(?im)^\s*CHECK:\s*(\S.*?)\s*$`)
	checksBlockOpener = regexp.MustCompile(`(?i)^\s*(?:` + "`{0,3}" + `)?\s*checks:\s*$`)
	checksListItem    = regexp.MustCompile(`^\s*[-*]\s+(\S.*?)\s*$`)
	dimensionPattern  = regexp.MustCompile(`(?im)^\s*DIMENSION:\s*(\S.*?)\s*$`)
)

// Checklist is the parsed AMD-20260916/6 checklist.
type Checklist struct {
	// Dimensions are the checklist dimensions the response must answer
	// satisfied | violated | not_applicable with a one-line reason.
	Dimensions []string
	// RequiredChecks are the commands the response must report an
	// execution record for: the command as run, and its exit code.
	RequiredChecks []string
}

// Empty reports whether the caller declared no checklist at all, in which
// case the reviewer adds no checklist instructions and demands no execution
// records (a caller that asked for nothing is not owed a violation).
func (c Checklist) Empty() bool {
	return len(c.Dimensions) == 0 && len(c.RequiredChecks) == 0
}

// ParseChecklist extracts the checklist from a caller's free-text Context.
// It never invents a dimension or a check: text with neither spelling
// yields the zero Checklist.
func ParseChecklist(reqContext string) Checklist {
	out := Checklist{}
	for _, m := range dimensionPattern.FindAllStringSubmatch(reqContext, -1) {
		out.Dimensions = append(out.Dimensions, m[1])
	}
	for _, m := range checkLinePattern.FindAllStringSubmatch(reqContext, -1) {
		out.RequiredChecks = append(out.RequiredChecks, m[1])
	}
	out.RequiredChecks = append(out.RequiredChecks, parseChecksBlocks(reqContext)...)
	return out
}

// parseChecksBlocks reads every fenced `checks:` list in text.
func parseChecksBlocks(text string) []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(text, "\n") {
		if checksBlockOpener.MatchString(line) {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if m := checksListItem.FindStringSubmatch(line); m != nil {
			out = append(out, strings.Trim(m[1], "`'\""))
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		inBlock = false
	}
	return out
}

// Instructions renders the prompt text that tells the model exactly what
// this checklist demands. It is appended to the level rubric, so it travels
// in the dispatched prompt and not merely in this process's memory.
func (c Checklist) Instructions() string {
	if c.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nCHECKLIST (AMD-20260916/6). Answer EVERY item below.\n")
	for _, d := range c.Dimensions {
		b.WriteString("- dimension " + d + ": answer satisfied | violated | not_applicable, plus a one-line reason.\n")
	}
	for _, chk := range c.RequiredChecks {
		b.WriteString("- required check " + chk + ": report the command you actually ran and its exit code. " +
			"A check with no execution record is a violation; never assert that it passed on someone else's word.\n")
	}
	b.WriteString(`Emit them in the structured response as "checklist":[{"name":...,"status":...,"reason":...}] ` +
		`and "executed_checks":[{"command":...,"exit_code":...}].`)
	return b.String()
}

// wireChecklistAnswer is one answered dimension in the structured response.
type wireChecklistAnswer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// wireExecutedCheck is one execution record: the command as run, and its
// exit code. ExitCode is a pointer so a response that omits it is
// distinguishable from one that reported 0 -- "it ran, exit 0" and "I did
// not say" must not collapse into the same value.
type wireExecutedCheck struct {
	Command  string `json:"command"`
	ExitCode *int   `json:"exit_code"`
}

// answeredStatuses is the closed vocabulary a dimension answer may use.
var answeredStatuses = map[string]bool{"satisfied": true, "violated": true, "not_applicable": true}

// unansweredDimensions returns the dimensions c declared that answers did
// not answer with a member of the closed status vocabulary.
func (c Checklist) unansweredDimensions(answers []wireChecklistAnswer) []string {
	answered := map[string]bool{}
	for _, a := range answers {
		if answeredStatuses[strings.ToLower(strings.TrimSpace(a.Status))] && strings.TrimSpace(a.Reason) != "" {
			answered[strings.ToLower(strings.TrimSpace(a.Name))] = true
		}
	}
	var missing []string
	for _, d := range c.Dimensions {
		if !answered[strings.ToLower(strings.TrimSpace(d))] {
			missing = append(missing, d)
		}
	}
	return missing
}

// unexecutedChecks returns the required checks with no execution record: no
// matching command, or a matching command with no exit code reported.
func (c Checklist) unexecutedChecks(executed []wireExecutedCheck) []string {
	recorded := map[string]bool{}
	for _, e := range executed {
		if e.ExitCode == nil {
			continue
		}
		recorded[strings.TrimSpace(e.Command)] = true
	}
	var missing []string
	for _, chk := range c.RequiredChecks {
		if !recorded[strings.TrimSpace(chk)] {
			missing = append(missing, chk)
		}
	}
	return missing
}

// Violations turns an unanswered dimension or an unexecuted required check
// into findings the caller receives. A required check with no execution
// record is severity major ("High" in the amendment's own wording; the ABI's
// four severities have no "high" member and major is the one that blocks
// unless explicitly waived) and cites D7 by name.
func (c Checklist) Violations(answers []wireChecklistAnswer, executed []wireExecutedCheck) []provider.ReviewFinding {
	if c.Empty() {
		return nil
	}
	var out []provider.ReviewFinding
	for _, d := range c.unansweredDimensions(answers) {
		out = append(out, provider.ReviewFinding{
			Severity: provider.ReviewSeverityMajor,
			Message: "unanswered checklist dimension: " + d +
				" -- AMD-20260916/6 requires satisfied | violated | not_applicable with a one-line reason",
		})
	}
	for _, chk := range c.unexecutedChecks(executed) {
		out = append(out, provider.ReviewFinding{
			Severity: provider.ReviewSeverityMajor,
			Message: unexecutedCheckTitle + ": " + chk +
				" -- no execution record (command plus exit code) accompanies this required check, " +
				"so it is a violated D7 finding (AMD-20260916/6, R-14.249): the reviewer never accepts " +
				"a worker's word that a check ran",
		})
	}
	return out
}
