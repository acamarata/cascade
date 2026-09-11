package repo

// Purpose: the go detector family. Evidence: go.mod, parsed with
//   golang.org/x/mod/modfile (the license-gated addition 06 §7
//   standing-authorizes for this exact purpose) so a malformed go.mod is
//   detected the same way the real go toolchain would reject it, never
//   by a self-authored regex dialect.
// SPORT: repo/language-detectors/ADD (P1-E33-W7-S67-T1).

import (
	"context"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"

	"github.com/acamarata/cascade/pkg/cascade"
)

type goDetector struct{}

func (goDetector) Detect(_ context.Context, root string) (LanguageFacts, error) {
	ok, err := evidenceExists(root, "go.mod")
	if err != nil {
		return LanguageFacts{}, err
	}
	if !ok {
		return LanguageFacts{Language: LanguageGo, Detected: false}, nil
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return LanguageFacts{}, cascade.Wrap(cascade.KindUnavailable, err, "repo: read go.mod")
	}
	return parseGoMod(data)
}

// parseGoMod parses raw go.mod bytes. Exported at package level (not a
// method) so FuzzDetectManifest can drive it directly with arbitrary
// bytes without needing a directory on disk.
func parseGoMod(data []byte) (LanguageFacts, error) {
	// Parse (strict), not ParseLax: ParseLax exists to tolerate an
	// UNKNOWN directive from a newer Go toolchain in a go.mod this
	// package only needs to read, not execute -- but that leniency also
	// silently accepts genuinely malformed content (e.g. an unbalanced
	// brace after an unrecognized line), which is exactly the case this
	// detector must fail closed on.
	if _, err := modfile.Parse("go.mod", data, nil); err != nil {
		return LanguageFacts{}, cascade.Wrap(cascade.KindInvalidInput, err, "repo: malformed go.mod")
	}
	return LanguageFacts{
		Language: LanguageGo,
		Detected: true,
		Commands: Commands{
			Build: "go build ./...",
			Test:  "go test ./...",
			Lint:  "golangci-lint run",
		},
		Evidence: []string{"go.mod"},
	}, nil
}
