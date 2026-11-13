package parity

// Summary holds the counts of each row kind in a parity ledger.
type Summary struct {
	Parity int
	Gap    int
	Waived int
}

// Summarize tallies the rows of a Matrix into a Summary by their Status.
func Summarize(m Matrix) Summary {
	var s Summary
	for _, r := range m.Rows {
		switch r.Status {
		case "parity":
			s.Parity++
		case "gap":
			s.Gap++
		case "waived":
			s.Waived++
		}
	}
	return s
}

// Zero reports whether the ledger has no gaps and at least one row.
func (s Summary) Zero() bool {
	return s.Gap == 0 && (s.Parity+s.Waived) > 0
}
