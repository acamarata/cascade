package mod

import "strings"

// ApplyNotesShare returns a new map with notes.share frontmatter keys normalized case-insensitively.
// When on is false, every key whose lowercase equals "share" is deleted before writing.
// When on is true, all case variants are removed and exactly one canonical "share" key is written.
// The input map is not mutated; other keys are untouched.
func ApplyNotesShare(front map[string]any, on bool) map[string]any {
	result := make(map[string]any, len(front))
	for k, v := range front {
		if strings.EqualFold(k, "share") {
			continue
		}
		result[k] = v
	}
	if on {
		result["share"] = true
	}
	return result
}
