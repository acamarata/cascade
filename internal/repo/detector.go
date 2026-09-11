package repo

// Purpose: the Detector interface, the ordered registry of the six
//   detector families, marker-file dispatch, and the Makefile-target
//   override rule R-16.37 states applies across every family ("existing
//   CI/Makefile targets override defaults").
// Inputs: DetectAll takes a context and a repository root directory.
// Outputs: one LanguageFacts per family that reports Detected=true (a
//   family with no evidence is simply omitted from the slice -- never a
//   zero-value LanguageFacts standing in for absence); a *cascade.Error
//   only for a genuine filesystem failure reading root itself. A family
//   whose evidence file IS present but malformed returns its own error
//   from DetectAll, since a malformed manifest is not a clean absence.
// Constraints: fail-closed -- a manifest DetectAll cannot parse is never
//   guessed into "generic" or silently dropped; it is a typed error.
//   When none of the five language-specific families detect evidence,
//   the generic fallback always fires and always returns Detected=true.
// SPORT: repo/language-detectors/ADD (P1-E33-W7-S67-T1).

import (
	"context"
	"os"
	"path/filepath"
	"regexp"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Detector classifies one repository root against one language family's
// evidence. Detect never panics: a well-formed absence returns
// (LanguageFacts{Detected:false}, nil); a present-but-malformed evidence
// file returns (LanguageFacts{}, typed error).
type Detector interface {
	Detect(ctx context.Context, root string) (LanguageFacts, error)
}

// registry is the fixed dispatch order: the five language-specific
// families first (each checked independently -- a repo can have more
// than one, e.g. a Go backend with a JS frontend), then the generic
// fallback runs only when none of them detected evidence.
func registry() []Detector {
	return []Detector{
		goDetector{},
		jstsDetector{},
		rustDetector{},
		pythonDetector{},
		swiftDetector{},
	}
}

// DetectAll runs every language-specific detector against root, then the
// generic fallback when none detected evidence, applying the Makefile-
// override rule to every result before returning.
func DetectAll(ctx context.Context, root string) ([]LanguageFacts, error) {
	if root == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: DetectAll requires a non-empty root")
	}
	overrides, err := makefileOverrides(root)
	if err != nil {
		return nil, err
	}

	var out []LanguageFacts
	for _, d := range registry() {
		facts, derr := d.Detect(ctx, root)
		if derr != nil {
			return nil, derr
		}
		if !facts.Detected {
			continue
		}
		out = append(out, applyOverrides(facts, overrides))
	}
	if len(out) == 0 {
		facts, gerr := (genericDetector{}).Detect(ctx, root)
		if gerr != nil {
			return nil, gerr
		}
		out = append(out, applyOverrides(facts, overrides))
	}
	return out, nil
}

// makefileTargetPattern matches a top-level Makefile target line
// ("build:", "test:", "lint:" -- optionally with prerequisites after the
// colon, never indented, matching GNU Make's own target-line grammar).
var makefileTargetPattern = regexp.MustCompile(`(?m)^(build|test|lint):`)

// makefileOverrides reads root/Makefile, if present, and returns the set
// of build/test/lint target names it defines. An absent Makefile is a
// clean empty result, never an error.
func makefileOverrides(root string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: read Makefile")
	}
	found := map[string]bool{}
	for _, m := range makefileTargetPattern.FindAllStringSubmatch(string(data), -1) {
		found[m[1]] = true
	}
	return found, nil
}

// applyOverrides replaces facts.Commands.{Build,Test,Lint} with `make
// <target>` for every target makefileOverrides found, per R-16.37's
// "existing CI/Makefile targets override defaults" rule. The generic
// family is exempt: its own Commands are already Makefile-derived (see
// detector_generic.go), so re-applying would just rewrap the same value.
func applyOverrides(facts LanguageFacts, overrides map[string]bool) LanguageFacts {
	if len(overrides) == 0 || facts.Language == LanguageGeneric {
		return facts
	}
	if overrides["build"] {
		facts.Commands.Build = "make build"
	}
	if overrides["test"] {
		facts.Commands.Test = "make test"
	}
	if overrides["lint"] {
		facts.Commands.Lint = "make lint"
	}
	return facts
}

// evidenceExists reports whether name exists as a regular file directly
// under root (never following a symlink -- Lstat, not Stat).
func evidenceExists(root, name string) (bool, error) {
	info, err := os.Lstat(filepath.Join(root, name))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "repo: stat %s", name)
	}
	return info.Mode().IsRegular(), nil
}
