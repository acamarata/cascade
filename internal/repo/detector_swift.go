package repo

// Purpose: the swift detector family. Evidence: Package.swift (checked
//   for the swift-tools-version directive SwiftPM itself requires as the
//   first non-comment line) OR a top-level *.xcodeproj/project.pbxproj
//   (checked for the pbxproj format marker Xcode itself always emits).
// SPORT: repo/language-detectors/ADD (P1-E33-W7-S67-T1).

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

type swiftDetector struct{}

var swiftToolsVersionMarker = []byte("swift-tools-version")

// pbxprojMarker is the header every real Xcode project file emits; a
// project.pbxproj missing it is not a genuine Xcode project file.
var pbxprojMarker = []byte("!$*UTF8*$!")

func (swiftDetector) Detect(_ context.Context, root string) (LanguageFacts, error) {
	pkgOK, err := evidenceExists(root, "Package.swift")
	if err != nil {
		return LanguageFacts{}, err
	}
	if pkgOK {
		data, rerr := os.ReadFile(filepath.Join(root, "Package.swift"))
		if rerr != nil {
			return LanguageFacts{}, cascade.Wrap(cascade.KindUnavailable, rerr, "repo: read Package.swift")
		}
		facts, perr := parsePackageSwift(data)
		if perr != nil {
			return LanguageFacts{}, perr
		}
		facts.Evidence = []string{"Package.swift"}
		return facts, nil
	}

	matches, gerr := filepath.Glob(filepath.Join(root, "*.xcodeproj"))
	if gerr != nil {
		return LanguageFacts{}, cascade.Wrap(cascade.KindInvalidInput, gerr, "repo: glob *.xcodeproj")
	}
	for _, dir := range matches {
		pbxPath := filepath.Join(dir, "project.pbxproj")
		data, rerr := os.ReadFile(pbxPath)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			return LanguageFacts{}, cascade.Wrap(cascade.KindUnavailable, rerr, "repo: read project.pbxproj")
		}
		facts, perr := parsePbxproj(data)
		if perr != nil {
			return LanguageFacts{}, perr
		}
		facts.Evidence = []string{filepath.Base(dir) + "/project.pbxproj"}
		return facts, nil
	}
	return LanguageFacts{Language: LanguageSwift, Detected: false}, nil
}

// parsePackageSwift verifies the swift-tools-version directive is
// present. Fuzz target for the Package.swift evidence shape.
func parsePackageSwift(data []byte) (LanguageFacts, error) {
	if !bytes.Contains(data, swiftToolsVersionMarker) {
		return LanguageFacts{}, cascade.New(cascade.KindInvalidInput, "repo: malformed Package.swift: missing swift-tools-version")
	}
	return swiftDefaults(), nil
}

// parsePbxproj verifies the pbxproj format marker is present. Fuzz target
// for the project.pbxproj evidence shape.
func parsePbxproj(data []byte) (LanguageFacts, error) {
	if !bytes.Contains(data, pbxprojMarker) {
		return LanguageFacts{}, cascade.New(cascade.KindInvalidInput, "repo: malformed project.pbxproj: missing format marker")
	}
	return swiftDefaults(), nil
}

func swiftDefaults() LanguageFacts {
	return LanguageFacts{
		Language: LanguageSwift,
		Detected: true,
		Commands: Commands{
			Build: "swift build",
			Test:  "swift test",
			Lint:  "swiftlint",
		},
	}
}
