package learn

// Purpose: tier classification for the closed proposal rule table.
// Inputs: ProposalRules rows.
// Outputs: one ConfigTier per row, each taken from ClassifyChange.
// Constraints: no second tier rule; a refused classification is an error.
// SPORT: learn/proposal-rules/ADD.

// classifyChange is ClassifyChange, held in a variable so a test can count
// calls.
var classifyChange = ClassifyChange

// ClassifyProposalTierTable returns the tier of each rule, in order. Each
// tier is the one ClassifyChange computes for the rule's target and change,
// called exactly once per rule.
func ClassifyProposalTierTable(rules []ProposalRule) ([]ConfigTier, error) {
	tiers := make([]ConfigTier, 0, len(rules))
	for _, r := range rules {
		c, err := classifyChange(string(r.Target), r.Change)
		if err != nil {
			return nil, err
		}
		tiers = append(tiers, c.Tier)
	}
	return tiers, nil
}
