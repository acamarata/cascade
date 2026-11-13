package mod

import (
	"sort"
	"strings"
)

// FindRailCallers scans the given file contents for the text 'cam rail'
// and returns sorted paths that reference it, excluding paths under legacyPrefix.
// An empty result means the audit passes.
func FindRailCallers(files map[string]string, legacyPrefix string) []string {
	var result []string
	for path, content := range files {
		if legacyPrefix != "" && strings.HasPrefix(path, legacyPrefix) {
			continue
		}
		if strings.Contains(content, "cam rail") {
			result = append(result, path)
		}
	}
	sort.Strings(result)
	return result
}
