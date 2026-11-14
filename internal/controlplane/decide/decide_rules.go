package decide

// invokeRules evaluates the rules for a question in slice order and returns
// the first matched verdict. If no rule matches, it returns an ESCALATE verdict.
func invokeRules(q Question, id string) (Verdict, error) {
	for _, r := range q.Rules {
		answer, matched := r(q)
		if matched {
			if answer != YES && answer != NO && answer != ESCALATE {
				return Verdict{}, ErrBadQuestion
			}
			return Verdict{
				Answer:     answer,
				Confidence: 1.0,
				ID:         id,
			}, nil
		}
	}

	return Verdict{
		Answer:     ESCALATE,
		Confidence: 0.0,
		ID:         id,
	}, nil
}
