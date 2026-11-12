// Package write contains control plane write policies.
package write

import "errors"

// PolicyFunc is a function that evaluates a policy for a given subject.
type PolicyFunc func(subject string) (bool, error)

// ErrD1147 is returned when a Conductor is instantiated without an injected PolicyFunc.
var ErrD1147 = errors.New("D1147: conductor requires an injected PolicyFunc")

// ErrInvalidSubject is returned when a policy evaluation is attempted with an invalid subject
// or when a Conductor fails closed due to missing internal state.
var ErrInvalidSubject = errors.New("invalid policy subject")

// Conductor evaluates write policies using an injected PolicyFunc.
type Conductor struct {
	policy PolicyFunc
}

// NewConductor creates a new Conductor with the provided PolicyFunc.
// If p is nil, it returns nil and ErrD1147.
func NewConductor(p PolicyFunc) (*Conductor, error) {
	if p == nil {
		return nil, ErrD1147
	}
	return &Conductor{policy: p}, nil
}

// Decide evaluates the policy for the given subject.
// If the conductor is nil or lacks a PolicyFunc, it fails closed.
func (c *Conductor) Decide(subject string) (bool, error) {
	if c == nil || c.policy == nil {
		return false, ErrInvalidSubject
	}
	if subject == "" {
		return false, ErrInvalidSubject
	}
	return c.policy(subject)
}
