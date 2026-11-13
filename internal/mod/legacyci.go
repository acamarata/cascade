package mod

import (
	"errors"
	"fmt"
	"strings"
)

// ErrLegacyFrozen indicates that a change was attempted in a frozen legacy CI module.
var ErrLegacyFrozen = errors.New("legacy CI module is frozen")

// CheckLegacyCI verifies whether any changed paths are located under the specified legacyDir.
// If any path falls under legacyDir, it returns an error wrapping ErrLegacyFrozen that lists
// the offending paths. Paths outside legacyDir pass, and an empty change list passes as well.
func CheckLegacyCI(changed []string, legacyDir string) error {
	var offending []string
	for _, p := range changed {
		if strings.HasPrefix(p, legacyDir) {
			offending = append(offending, p)
		}
	}
	if len(offending) > 0 {
		return fmt.Errorf("%w: %s", ErrLegacyFrozen, strings.Join(offending, ", "))
	}
	return nil
}
