// Purpose: the two extraction primitives ParseMarkerLine builds on:
//
//	splitting one marker's text into candidate entity segments, and
//	pulling a name/status pair out of one segment.
//
// SPORT: internal.inventory.sport.splitTopLevelSegments/ADDED.

package sport

import "strings"

// splitTopLevelSegments splits text on commas OUTSIDE any paren/bracket
// nesting — a comma inside "(test fakes, P1-E17-W4-S36-T4)" is that one
// entity's own detail text, not a separator, matching doc.go's worked
// example ("a/ADDED, b/ADDED, c/ADDED" — three top-level commas, three
// entities).
//
// Splitting is only trusted when at least one resulting segment carries a
// recognized status verb: a status-free line ("cmd/cascade — cobra-root,
// global-flags, version, completions.") is prose with commas of its own,
// not a multi-entity list, and splitting it would manufacture fake
// entities ("global-flags", "version") that were never separately
// SPORT-tagged. When no segment has a status verb, the whole text is
// returned as one segment instead.
func splitTopLevelSegments(text string) []string {
	raw := splitOnTopLevelCommas(text)
	if len(raw) <= 1 || hasStatusWord(raw) {
		return raw
	}
	return []string{text}
}

// splitOnTopLevelCommas is the depth-tracking comma split itself.
func splitOnTopLevelCommas(text string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range text {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, text[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, text[start:])
	return out
}

// nameCutset is trimmed from a candidate name's edges after boundary
// detection: separators, trailing punctuation, and whitespace that never
// belong on either end of a real name.
const nameCutset = " \t([:—·/.,"

// extractNameAndStatus finds seg's status verb (anywhere in the segment)
// and its name boundary (the earliest separator OR the status verb's own
// position, whichever comes first — see doc.go's worked examples), then
// applies the repo's one recurring "placeholder:" prefix convention
// (internal/doctor's `SPORT: placeholder: doctor/bundle (ADD).`) by
// re-running on the remainder when the extracted name is literally
// "placeholder".
func extractNameAndStatus(seg string) (name, status string) {
	status = normalizedStatus(seg)
	boundary := len(seg)
	if loc := separatorPattern.FindStringIndex(seg); loc != nil && loc[0] < boundary {
		boundary = loc[0]
	}
	if loc := statusWordPattern.FindStringIndex(seg); loc != nil && loc[0] < boundary {
		boundary = loc[0]
	}
	name = strings.Trim(seg[:boundary], nameCutset)
	if strings.EqualFold(name, "placeholder") && boundary < len(seg) {
		rest := strings.TrimLeft(seg[boundary:], nameCutset)
		if rest != "" {
			return extractNameAndStatus(rest)
		}
	}
	return name, status
}

// normalizedStatus returns statusAliases' normalized form for the first
// recognized status verb in seg, or StatusUnspecified.
func normalizedStatus(seg string) string {
	m := statusWordPattern.FindString(seg)
	if m == "" {
		return StatusUnspecified
	}
	return statusAliases[m]
}
