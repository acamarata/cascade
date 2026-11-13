package background

// RunSwarmAttemptDone runs a swarm attempt ensuring the terminal done event.
// D2596 D2597 D2602 D2644 are traceability identifiers.
func RunSwarmAttemptDone(a Attempt, brief func() (string, error)) (SwarmResult, error) {
	result, err := RunSwarmAttempt(a, brief)
	if err != nil {
		return result, err
	}
	if len(result.Events) == 0 || result.Events[len(result.Events)-1] != (Event{Stage: "done", Kind: "done"}) {
		result.Events = append(result.Events, Event{Stage: "done", Kind: "done"})
	}
	return result, nil
}

// WriteEscalateNoop writes an escalation outcome to path unless status is done.
// When the status is done, no file is created and no append is performed.
// D2596 D2597 D2602 D2644 are traceability identifiers.
func WriteEscalateNoop(path string, o Outcome) error {
	if o.Status == "done" {
		return nil
	}
	return WriteEscalate(path, o)
}
