package repo

// Purpose: the generic fallback family. Runs only when none of the five
//   language-specific detectors found evidence (detector.go's DetectAll).
//   It always reports Detected=true -- "generic" is itself the fact, not
//   an absence -- and its commands come from the repo's own Makefile
//   targets when present, else are empty strings (never a guessed
//   command for a stack this package cannot identify).
// SPORT: repo/language-detectors/ADD (P1-E33-W7-S67-T1).

import "context"

type genericDetector struct{}

func (genericDetector) Detect(_ context.Context, root string) (LanguageFacts, error) {
	overrides, err := makefileOverrides(root)
	if err != nil {
		return LanguageFacts{}, err
	}
	readmeOK, err := readmePresent(root)
	if err != nil {
		return LanguageFacts{}, err
	}

	var evidence []string
	if len(overrides) > 0 {
		evidence = append(evidence, "Makefile")
	}
	if readmeOK {
		evidence = append(evidence, "README")
	}

	cmds := Commands{}
	if overrides["build"] {
		cmds.Build = "make build"
	}
	if overrides["test"] {
		cmds.Test = "make test"
	}
	if overrides["lint"] {
		cmds.Lint = "make lint"
	}

	return LanguageFacts{
		Language: LanguageGeneric,
		Detected: true,
		Commands: cmds,
		Evidence: evidence,
	}, nil
}

// genericReadmeNames is the closed set of README spellings this detector
// recognizes, matching the common repo-root conventions.
var genericReadmeNames = []string{"README.md", "README", "README.txt", "README.rst"}

func readmePresent(root string) (bool, error) {
	for _, name := range genericReadmeNames {
		ok, err := evidenceExists(root, name)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
