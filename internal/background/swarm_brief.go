package background

// RunSwarmAttemptBrief builds the next brief through RunSwarmAttempt, which
// appends VerifyTail with two newlines only when both base and tail are nonempty.
// Attempt errors remain in the outcome, separate from the brief and events.
func RunSwarmAttemptBrief(a Attempt, brief func() (string, error)) (SwarmResult, error) {
	return RunSwarmAttempt(a, brief)
}
