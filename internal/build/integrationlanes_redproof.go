// Purpose: the R18 M7 workflow-shape lint for the integration-lane red proof
// (split out of integrationlanes_table.go to keep that file under the 300-line cap).

package build

import (
	"fmt"
	"slices"
	"strings"
)

// redProofProblems checks the R18 M7 shape: the lane job and the assert job
// block ci-gate; the red-proof job is not in ci-gate needs, never soft-fails at
// job level, and its report upload runs even when the seeded step fails. It
// passes when the seeded row fails: exactly one step carries continue-on-error,
// it has an id, and a later step's run reads steps.<id>.outcome.
func redProofProblems(wf ciWorkflow) []string {
	var out []string
	add := func(m string) { out = append(out, "red proof: "+m) }
	gate := wf.Jobs[CIGateJob].Needs
	if lane, ok := wf.Jobs[LaneJob]; !ok || !slices.Contains(gate, LaneJob) || softFail(lane) {
		add(LaneJob + " must exist, be in ci-gate needs and not have continue-on-error")
	}
	if proof, ok := wf.Jobs[RedProofJob]; !ok {
		add(RedProofJob + " job is missing")
	} else {
		uploads := 0
		for _, s := range proof.Steps {
			if strings.HasPrefix(s.Uses, "actions/upload-artifact") {
				uploads++
				if !alwaysIf(s.If) {
					add(RedProofJob + " upload step lacks if: always()")
				}
			}
		}
		if uploads == 0 {
			add(RedProofJob + " has no report upload step")
		}
		if softFail(proof) {
			add(RedProofJob + " has continue-on-error")
		}
		for _, m := range redProofStepProblems(proof) {
			add(m)
		}
		if slices.Contains(gate, RedProofJob) {
			add(RedProofJob + " must not be in ci-gate needs (it is expected to fail)")
		}
	}
	assert, ok := wf.Jobs[RedAssertJob]
	switch {
	case !ok:
		add(RedAssertJob + " job is missing")
	case !slices.Contains(assert.Needs, RedProofJob):
		add(RedAssertJob + " does not need " + RedProofJob)
	case !alwaysIf(assert.If):
		add(RedAssertJob + " lacks if: always()")
	}
	if ok && !slices.Contains(gate, RedAssertJob) {
		add(RedAssertJob + " is not in ci-gate needs")
	}
	return out
}

// redProofStepProblems checks the inverted red-proof step shape.
func redProofStepProblems(proof ciJob) []string {
	var soft []int
	for i, s := range proof.Steps {
		if softFail(ciJob{ContinueOnError: s.ContinueOnError}) {
			soft = append(soft, i)
		}
	}
	if len(soft) != 1 {
		return []string{fmt.Sprintf("%s must have exactly one step with continue-on-error, has %d", RedProofJob, len(soft))}
	}
	at := soft[0]
	id := proof.Steps[at].ID
	if id == "" {
		return []string{RedProofJob + " continue-on-error step has no id"}
	}
	ref := "steps." + id + ".outcome"
	for _, s := range proof.Steps[at+1:] {
		if strings.Contains(s.Run, ref) {
			return nil
		}
	}
	return []string{RedProofJob + " has no later step checking " + ref}
}
