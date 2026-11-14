package consult

// Vote represents a vote cast by a voter with a holds value.
type Vote struct {
	Voter string
	HOLDS string
}

// HoldsDecision decides whether a proposal holds based on the votes.
// It returns true only if there are at least 2 yes votes and strictly more yes votes than no votes.
// Votes with unrecognised HOLDS values are ignored.
func HoldsDecision(votes []Vote) bool {
	yes := 0
	no := 0
	for _, v := range votes {
		switch v.HOLDS {
		case "yes":
			yes++
		case "no":
			no++
		}
	}
	return yes >= 2 && yes > no
}
