// Package swarm runs swarm rounds, calling a split pass between rounds and assigning ladder splits.
package swarm

import "errors"

// SplitPass is a function called between autonomy rounds.
type SplitPass func() (int, error)

// Ladder holds state accumulated across rounds.
type Ladder struct {
	// Splits stores the result of the last split pass.
	Splits int
}

// RunRounds executes round for the given number of rounds, calling split between each.
func RunRounds(rounds int, round func(i int) error, split SplitPass, ladder *Ladder) error {
	for i := 0; i < rounds; i++ {
		if err := round(i); err != nil {
			return err
		}
		if i < rounds-1 {
			n, err := split()
			if err != nil {
				return err
			}
			ladder.Splits = n
		}
	}
	return nil
}

// ErrSplit is returned when a split pass fails.
var ErrSplit = errors.New("split error")
