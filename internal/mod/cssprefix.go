// Package mod provides module utilities for cascade.
package mod

import (
	"fmt"
	"strings"
)

// ValidatePrefixes checks that CSS class names in settings and rail groups
// have distinct prefixes. Each group must use exactly one prefix, which is
// derived as the text before the first hyphen. Empty groups or groups with
// mixed prefixes fail closed and return an error.
func ValidatePrefixes(settings, rail []string) error {
	sp, err := groupPrefix("settings", settings)
	if err != nil {
		return err
	}
	rp, err := groupPrefix("rail", rail)
	if err != nil {
		return err
	}
	if sp == rp {
		return fmt.Errorf("settings and rail share prefix %q", sp)
	}
	return nil
}

func groupPrefix(name string, classes []string) (string, error) {
	if len(classes) == 0 {
		return "", fmt.Errorf("%s group is empty", name)
	}
	var prefix string
	for i, c := range classes {
		p := prefixOf(c)
		if p == "" {
			return "", fmt.Errorf("%s class %q has no prefix", name, c)
		}
		if i == 0 {
			prefix = p
		} else if prefix != p {
			return "", fmt.Errorf("%s group has mixed prefixes: %q and %q", name, prefix, p)
		}
	}
	return prefix, nil
}

func prefixOf(class string) string {
	idx := strings.Index(class, "-")
	if idx < 0 {
		return ""
	}
	return class[:idx]
}
