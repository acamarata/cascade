// Package consult provides model routing for workflow roles.
package consult

import "errors"

// ErrNoModel is returned when no model can be resolved for a role.
var ErrNoModel = errors.New("no model configured for role")

// Chooser selects a model for a role using a default matrix and owner overrides.
type Chooser struct {
	matrix    map[string]string
	overrides map[string]string
}

// NewChooser returns a Chooser that reads defaults from matrix and applies overrides.
// Neither map is mutated or retained by reference beyond read access.
func NewChooser(matrix, overrides map[string]string) *Chooser {
	return &Chooser{matrix: matrix, overrides: overrides}
}

// Choose resolves the model for role. An override wins over the matrix default.
// If neither source contains the role, it returns ErrNoModel.
func (c *Chooser) Choose(role string) (string, error) {
	if c.overrides != nil {
		if m, ok := c.overrides[role]; ok {
			return m, nil
		}
	}
	if c.matrix != nil {
		if m, ok := c.matrix[role]; ok {
			return m, nil
		}
	}
	return "", ErrNoModel
}
