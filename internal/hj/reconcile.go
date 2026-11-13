// Package hj provides nodelink reconciliation logic.
package hj

import "errors"

// Offer represents an offer made to a node.
type Offer struct {
	ID         string
	Dispatched bool
}

// Attempt represents an attempt dispatched to a node.
type Attempt struct {
	ID string
}

// State represents the current state of offers and attempts.
type State struct {
	Init     bool
	Offers   []Offer
	Attempts []Attempt
}

// Reconcile revokes undispatched offers and keeps dispatched attempts.
// It returns an error if the state is not initialized.
func Reconcile(s State) (State, error) {
	if !s.Init {
		return State{}, errors.New("D0913: invalid state")
	}

	var activeOffers []Offer
	for _, o := range s.Offers {
		if o.Dispatched {
			activeOffers = append(activeOffers, o)
		}
	}

	s.Offers = activeOffers
	return s, nil
}
