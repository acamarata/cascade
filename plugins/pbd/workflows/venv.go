package workflows

import (
	"errors"
)

// PytestVersion is the pinned version of pytest used by the workflows.
const PytestVersion = "8.3.5"

// ErrPythonUnavailable indicates that no suitable Python interpreter could be found.
var ErrPythonUnavailable = errors.New("D5009")

// Env abstracts filesystem and process operations needed by workflows.
type Env interface {
	Exists(p string) bool
	Run(name string, args ...string) error
}

// FindPython examines candidates in order, restricted to exact /opt/homebrew/bin/python3
// and /usr/bin/python3. Probe returns nil only when executable supports venv and ensurepip.
// Nil/empty candidates, nil probe, duplicate or unsupported candidates return "",ErrPythonUnavailable
// before any probe. Probe failures continue, first success wins, exhausted list returns
// "",ErrPythonUnavailable.
func FindPython(cands []string, probe func(string) error) (string, error) {
	if len(cands) == 0 {
		return "", ErrPythonUnavailable
	}
	if probe == nil {
		return "", ErrPythonUnavailable
	}

	allowed := map[string]bool{
		"/opt/homebrew/bin/python3": true,
		"/usr/bin/python3":          true,
	}
	seen := map[string]bool{}
	for _, c := range cands {
		if !allowed[c] {
			return "", ErrPythonUnavailable
		}
		if seen[c] {
			return "", ErrPythonUnavailable
		}
		seen[c] = true
	}

	for _, c := range cands {
		if probe(c) == nil {
			return c, nil
		}
	}
	return "", ErrPythonUnavailable
}
