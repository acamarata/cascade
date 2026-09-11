package repo

// Purpose: the rust detector family. Evidence: Cargo.toml, parsed via the
//   TOML dependency internal/config's loader already carries
//   (github.com/pelletier/go-toml/v2, a direct module requirement --
//   C/S-04.T1), so no new TOML dialect is introduced.
// SPORT: repo/language-detectors/ADD (P1-E33-W7-S67-T1).

import (
	"context"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/pkg/cascade"
)

type rustDetector struct{}

func (rustDetector) Detect(_ context.Context, root string) (LanguageFacts, error) {
	ok, err := evidenceExists(root, "Cargo.toml")
	if err != nil {
		return LanguageFacts{}, err
	}
	if !ok {
		return LanguageFacts{Language: LanguageRust, Detected: false}, nil
	}
	data, err := os.ReadFile(filepath.Join(root, "Cargo.toml"))
	if err != nil {
		return LanguageFacts{}, cascade.Wrap(cascade.KindUnavailable, err, "repo: read Cargo.toml")
	}
	return parseCargoToml(data)
}

// parseCargoToml parses raw Cargo.toml bytes. It only verifies TOML
// syntax validity -- Cargo.toml's own schema (package/workspace tables)
// is not this detector's concern; presence + syntactic validity is the
// R-16.37 evidence contract.
func parseCargoToml(data []byte) (LanguageFacts, error) {
	var v map[string]any
	if err := toml.Unmarshal(data, &v); err != nil {
		return LanguageFacts{}, cascade.Wrap(cascade.KindInvalidInput, err, "repo: malformed Cargo.toml")
	}
	return LanguageFacts{
		Language: LanguageRust,
		Detected: true,
		Commands: Commands{
			Build: "cargo build",
			Test:  "cargo test",
			Lint:  "cargo clippy",
		},
		Evidence: []string{"Cargo.toml"},
	}, nil
}
