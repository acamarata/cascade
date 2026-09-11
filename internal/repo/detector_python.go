package repo

// Purpose: the python detector family. Evidence: pyproject.toml OR
//   requirements.txt (R-16.37 lists both as evidence for this one
//   family). pyproject.toml is preferred when both are present, since it
//   is the more authoritative modern manifest.
// SPORT: repo/language-detectors/ADD (P1-E33-W7-S67-T1).

import (
	"context"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/pkg/cascade"
)

type pythonDetector struct{}

func (pythonDetector) Detect(_ context.Context, root string) (LanguageFacts, error) {
	pyprojectOK, err := evidenceExists(root, "pyproject.toml")
	if err != nil {
		return LanguageFacts{}, err
	}
	if pyprojectOK {
		data, rerr := os.ReadFile(filepath.Join(root, "pyproject.toml"))
		if rerr != nil {
			return LanguageFacts{}, cascade.Wrap(cascade.KindUnavailable, rerr, "repo: read pyproject.toml")
		}
		facts, perr := parsePyprojectToml(data)
		if perr != nil {
			return LanguageFacts{}, perr
		}
		facts.Evidence = []string{"pyproject.toml"}
		return facts, nil
	}

	reqOK, err := evidenceExists(root, "requirements.txt")
	if err != nil {
		return LanguageFacts{}, err
	}
	if !reqOK {
		return LanguageFacts{Language: LanguagePython, Detected: false}, nil
	}
	facts := pythonDefaults()
	facts.Evidence = []string{"requirements.txt"}
	return facts, nil
}

// parsePyprojectToml parses raw pyproject.toml bytes, verifying TOML
// syntax validity only (per the same evidence contract as Cargo.toml).
func parsePyprojectToml(data []byte) (LanguageFacts, error) {
	var v map[string]any
	if err := toml.Unmarshal(data, &v); err != nil {
		return LanguageFacts{}, cascade.Wrap(cascade.KindInvalidInput, err, "repo: malformed pyproject.toml")
	}
	return pythonDefaults(), nil
}

func pythonDefaults() LanguageFacts {
	return LanguageFacts{
		Language: LanguagePython,
		Detected: true,
		Commands: Commands{
			Build: "python -m build",
			Test:  "pytest",
			Lint:  "ruff check",
		},
	}
}
