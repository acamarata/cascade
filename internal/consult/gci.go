// Package consult provides scoped GCI loading utilities.
package consult

import (
	"fmt"
	"regexp"
	"strings"
)

// KeysForMessage finds @key tokens in a message.
func KeysForMessage(msg string) []string {
	re := regexp.MustCompile(`@([a-zA-Z0-9_-]+)`)
	matches := re.FindAllStringSubmatch(msg, -1)
	keys := make([]string, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			keys = append(keys, m[1])
		}
	}
	return keys
}

// LoadScopedSections returns only the matched blocks relevant to the current message.
func LoadScopedSections(gci, msg string) (string, error) {
	keys := KeysForMessage(msg)
	if len(keys) == 0 {
		return "", nil
	}

	var result strings.Builder
	for _, key := range keys {
		startTag := fmt.Sprintf("<!-- binding:%s -->", key)
		endTag := "<!-- /binding -->"
		startIdx := strings.Index(gci, startTag)
		if startIdx == -1 {
			return "", fmt.Errorf("M1010: unknown binding section \"%s\"", key)
		}
		endIdx := strings.Index(gci[startIdx+len(startTag):], endTag)
		if endIdx == -1 {
			return "", fmt.Errorf("M1010: unknown binding section \"%s\"", key)
		}
		endIdx += startIdx + len(startTag) + len(endTag)
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString(gci[startIdx:endIdx])
	}
	return result.String(), nil
}
