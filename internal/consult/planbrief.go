package consult

import (
	"errors"
	"strings"
)

// ErrEmptyBrief is returned when the base brief is empty.
var ErrEmptyBrief = errors.New("consult: empty brief")

const depHint = "Do not redeclare types already defined in these dependency files:"

// BuildPlanningBrief constructs a planning brief by appending dependency
// hints to the base string when dependency files are present.
func BuildPlanningBrief(base string, depFiles []string) (string, error) {
	if base == "" {
		return "", ErrEmptyBrief
	}
	if len(depFiles) == 0 {
		return base, nil
	}

	var sb strings.Builder
	sb.WriteString(base)
	sb.WriteString("\n\n")
	sb.WriteString(depHint)
	sb.WriteString("\n")
	for _, f := range depFiles {
		sb.WriteString(f)
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}
