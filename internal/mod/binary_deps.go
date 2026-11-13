package mod

import (
	"fmt"
)

// ErrForbiddenDep indicates that a binary imported a package it is not allowed to.
type ErrForbiddenDep struct {
	Binary string
	Pkg    string
}

func (e *ErrForbiddenDep) Error() string {
	return fmt.Sprintf("binary %s imports forbidden package %s", e.Binary, e.Pkg)
}

// ErrMissingBinary indicates that a required binary is missing from the dependency map.
type ErrMissingBinary struct {
	Binary string
}

func (e *ErrMissingBinary) Error() string {
	return fmt.Sprintf("missing required binary: %s", e.Binary)
}

// CheckBinaryDeps validates the imported packages of capp-hook and capp-decide.
// capp-hook must not import net/http.
// capp-hook must be present if capp-decide is present.
func CheckBinaryDeps(deps map[string][]string) error {
	hookPkgs, hookExists := deps["capp-hook"]
	_, decideExists := deps["capp-decide"]

	if decideExists && !hookExists {
		return &ErrMissingBinary{Binary: "capp-hook"}
	}

	if hookExists {
		for _, pkg := range hookPkgs {
			if pkg == "net/http" {
				return &ErrForbiddenDep{Binary: "capp-hook", Pkg: "net/http"}
			}
		}
	}

	return nil
}
