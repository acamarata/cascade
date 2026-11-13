package mod

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// ParseModuleNames extracts module names from a MODULES.md byte slice.
// It looks for level-2 and level-3 headings (## Name or ### Name).
func ParseModuleNames(md []byte) map[string]bool {
	names := make(map[string]bool)
	lines := bytes.Split(md, []byte("\n"))
	for _, line := range lines {
		s := string(line)
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "### ") {
			name := strings.TrimSpace(strings.TrimPrefix(s, "### "))
			if name != "" {
				names[name] = true
			}
		} else if strings.HasPrefix(s, "## ") {
			name := strings.TrimSpace(strings.TrimPrefix(s, "## "))
			if name != "" {
				names[name] = true
			}
		}
	}
	return names
}

var modTokenRegex = regexp.MustCompile(`mod:([A-Za-z0-9_-]+)`)

// CheckModuleNames checks if all mod:Name tokens in the lane code are defined in MODULES.md.
// It returns a list of violations (names not found) and an error if mdPath cannot be read.
func CheckModuleNames(mdPath string, lane map[string]string) ([]string, error) {
	md, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, fmt.Errorf("reading modules md: %w", err)
	}

	defined := ParseModuleNames(md)
	violationsMap := make(map[string]bool)

	for _, code := range lane {
		matches := modTokenRegex.FindAllStringSubmatch(code, -1)
		for _, match := range matches {
			if len(match) > 1 {
				name := match[1]
				if !defined[name] {
					violationsMap[name] = true
				}
			}
		}
	}

	var violations []string
	for v := range violationsMap {
		violations = append(violations, v)
	}
	sort.Strings(violations)

	return violations, nil
}
