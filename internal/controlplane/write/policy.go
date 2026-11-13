// Package write contains control plane write policies and conductors.
package write

import (
	"errors"
	"strings"
)

// PolicyFunc is a function that evaluates a policy for a given subject.
type PolicyFunc func(subject string) (bool, error)

// ErrD1147 is returned when a Conductor is instantiated without an injected PolicyFunc.
var ErrD1147 = errors.New("D1147: conductor requires an injected PolicyFunc")

// ErrInvalidSubject is returned when a policy evaluation is attempted with an invalid subject.
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
// If the conductor is nil or lacks a PolicyFunc, it fails closed with ErrD1147.
// If the subject is empty or whitespace, it fails closed with ErrInvalidSubject.
func (c *Conductor) Decide(subject string) (bool, error) {
	if c == nil || c.policy == nil {
		return false, ErrD1147
	}
	if strings.TrimSpace(subject) == "" {
		return false, ErrInvalidSubject
	}
	return c.policy(subject)
}
