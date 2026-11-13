package docket

import "errors"

// Brief represents a set of concrete identifiers required for a brief.
type Brief struct {
	Files []string
	Funcs []string
	Types []string
	Tests []string
}

var (
	// ErrBriefVague is returned when a brief contains a vague placeholder instead of a concrete identifier.
	ErrBriefVague = errors.New("brief contains vague placeholder")
	// ErrBriefMissingIdent is returned when a brief lacks a required concrete identifier.
	ErrBriefMissingIdent = errors.New("brief missing required identifier")
)

// vaguePlaceholders lists strings that are too vague to be accepted as concrete identifiers.
var vaguePlaceholders = []string{
	"the file",
	"see above",
	"relevant tests",
	"etc",
}

// ValidateBrief checks that a brief names at least one exact file path, one function name,
// one type name and one test name, each non-empty. It fails closed with ErrBriefVague if
// any field contains a vague placeholder, or ErrBriefMissingIdent if any field is empty.
func ValidateBrief(b Brief) error {
	if len(b.Files) == 0 {
		return ErrBriefMissingIdent
	}
	if len(b.Funcs) == 0 {
		return ErrBriefMissingIdent
	}
	if len(b.Types) == 0 {
		return ErrBriefMissingIdent
	}
	if len(b.Tests) == 0 {
		return ErrBriefMissingIdent
	}

	for _, s := range b.Files {
		if isVague(s) {
			return ErrBriefVague
		}
	}
	for _, s := range b.Funcs {
		if isVague(s) {
			return ErrBriefVague
		}
	}
	for _, s := range b.Types {
		if isVague(s) {
			return ErrBriefVague
		}
	}
	for _, s := range b.Tests {
		if isVague(s) {
			return ErrBriefVague
		}
	}

	return nil
}

func isVague(s string) bool {
	for _, p := range vaguePlaceholders {
		if s == p {
			return true
		}
	}
	return false
}
