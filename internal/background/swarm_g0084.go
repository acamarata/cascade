// Package background provides swarm branch utilities.
package background

import (
	"errors"
	"regexp"
)

// ErrInvalidSwarmBranch is returned when inputs for swarm branch creation are invalid.
var ErrInvalidSwarmBranch = errors.New("invalid swarm branch")

// GitOps defines the interface for local git operations used by swarm branch creation.
type GitOps interface {
	// RefExists reports whether the given ref exists in the repository.
	RefExists(ref string) bool
	// CreateBranch creates a new branch with the given name from the specified base.
	CreateBranch(name, base string) error
}

var validComponentRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func isValidComponent(s string) bool {
	return validComponentRe.MatchString(s)
}

func isValidBase(base string) bool {
	if base == "" {
		return true
	}
	for i := 0; i < len(base); i++ {
		if base[i] <= ' ' {
			return false
		}
	}
	parts := splitSlash(base)
	for _, p := range parts {
		if !isValidComponent(p) {
			return false
		}
	}
	return true
}

func splitSlash(s string) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

// CreateSwarmBranch creates a local swarm branch from the given base.
// If base is empty, it defaults to "master". The branch name is exactly "swarm/"+run+"/"+id.
// run and id must each match ^[A-Za-z0-9][A-Za-z0-9_-]*$.
// Base accepts slash-separated nonempty components matching that same expression.
// Validation order: nil g, invalid base/run/id returns ErrInvalidSwarmBranch with zero calls.
// All failures return an empty branch name.
func CreateSwarmBranch(g GitOps, base, run, id string) (string, error) {
	if g == nil {
		return "", ErrInvalidSwarmBranch
	}
	if !isValidBase(base) {
		return "", ErrInvalidSwarmBranch
	}
	if !isValidComponent(run) {
		return "", ErrInvalidSwarmBranch
	}
	if !isValidComponent(id) {
		return "", ErrInvalidSwarmBranch
	}

	actualBase := base
	if actualBase == "" {
		actualBase = "master"
	}

	branchName := "swarm/" + run + "/" + id

	if !g.RefExists(actualBase) {
		return "", ErrInvalidSwarmBranch
	}

	if g.RefExists(branchName) {
		return branchName, nil
	}

	err := g.CreateBranch(branchName, actualBase)
	if err != nil {
		return "", ErrInvalidSwarmBranch
	}

	return branchName, nil
}
