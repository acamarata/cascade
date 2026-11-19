package mod

import (
	"errors"
	"strings"
)

// ErrLegacyPrefix is returned when a path uses the deprecated .github/ai/ prefix.
var ErrLegacyPrefix = errors.New("legacy .github/ai/ prefix detected")

// LanePath returns the native repository path for the given lane.
// For the skills-assets lane, it returns "skills-assets" directly,
// without any .github/ai prefix.
func LanePath(lane string) string {
	if lane == "skills-assets" {
		return "skills-assets"
	}
	return lane
}

// CheckNoGithubAIPrefix returns ErrLegacyPrefix if any of the given paths
// starts with ".github/ai/". It returns nil if all paths use the native location.
func CheckNoGithubAIPrefix(paths []string) error {
	for _, p := range paths {
		if strings.HasPrefix(p, ".github/ai/") {
			return ErrLegacyPrefix
		}
	}
	return nil
}
