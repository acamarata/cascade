package repo

// Purpose: the js/ts detector family. Evidence: package.json plus a
//   lockfile; pnpm-lock.yaml selects the pnpm defaults (R-16.37), any
//   other recognized lockfile keeps the pnpm defaults too since 06 §7
//   standard-izes pnpm as this project's own JS tooling, but package.json
//   alone with no lockfile is still Detected -- a fresh repo before its
//   first install is not "no js/ts here".
// SPORT: repo/language-detectors/ADD (P1-E33-W7-S67-T1).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

type jstsDetector struct{}

var jstsLockfiles = []string{"pnpm-lock.yaml", "package-lock.json", "yarn.lock"}

func (jstsDetector) Detect(_ context.Context, root string) (LanguageFacts, error) {
	ok, err := evidenceExists(root, "package.json")
	if err != nil {
		return LanguageFacts{}, err
	}
	if !ok {
		return LanguageFacts{Language: LanguageJSTS, Detected: false}, nil
	}
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return LanguageFacts{}, cascade.Wrap(cascade.KindUnavailable, err, "repo: read package.json")
	}
	facts, perr := parsePackageJSON(data)
	if perr != nil {
		return LanguageFacts{}, perr
	}

	evidence := []string{"package.json"}
	for _, lock := range jstsLockfiles {
		lockOK, lerr := evidenceExists(root, lock)
		if lerr != nil {
			return LanguageFacts{}, lerr
		}
		if lockOK {
			evidence = append(evidence, lock)
			break
		}
	}
	facts.Evidence = evidence
	return facts, nil
}

// parsePackageJSON parses raw package.json bytes and returns the pnpm
// default command set. It only verifies the manifest is well-formed JSON
// object syntax -- it never inspects a "scripts" field to guess commands,
// since R-16.37's defaults are fixed per family, not derived per-repo.
func parsePackageJSON(data []byte) (LanguageFacts, error) {
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		return LanguageFacts{}, cascade.Wrap(cascade.KindInvalidInput, err, "repo: malformed package.json")
	}
	return LanguageFacts{
		Language: LanguageJSTS,
		Detected: true,
		Commands: Commands{
			Build:   "pnpm build",
			Test:    "pnpm test",
			Lint:    "pnpm lint",
			Package: "pnpm",
		},
	}, nil
}
