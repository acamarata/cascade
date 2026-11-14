package background

import (
	"strings"
	"unicode/utf8"
)

// scanDiff validates a unified diff string against the allowed file set.
// It scans line-by-line in O(len(diff)) without regex backtracking.
func scanDiff(diff string, allowed map[string]bool) error {
	if diff == "" {
		return ErrEgress
	}
	if !utf8.ValidString(diff) {
		return ErrEgress
	}

	lines := strings.Split(diff, "\n")
	// Allow empty trailing element from final LF.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ErrEgress
	}

	covered := make(map[string]bool, len(allowed))
	state := 0 // 0=expect header, 1=expect ---, 2=expect +++, 3=expect @@, 4=in hunk
	var curPath string

	for _, line := range lines {
		if strings.ContainsRune(line, '\x00') || strings.ContainsRune(line, '\r') {
			return ErrEgress
		}
		if strings.HasPrefix(line, "diff --cc ") ||
			strings.HasPrefix(line, "diff --combined ") ||
			strings.HasPrefix(line, "@@@") ||
			strings.HasPrefix(line, "GIT binary patch") ||
			strings.HasPrefix(line, "Binary files ") {
			return ErrEgress
		}

		if strings.HasPrefix(line, "diff --git ") {
			if state > 0 && state < 4 {
				return ErrEgress
			}
			if state == 4 {
				covered[curPath] = true
			}
			p, err := parseHeaderPath(line)
			if err != nil {
				return err
			}
			if covered[p] {
				return ErrEgress
			}
			if !allowed[p] {
				return ErrEgress
			}
			curPath = p
			state = 1
			continue
		}

		switch state {
		case 0:
			return ErrEgress
		case 1:
			if line != "--- a/"+curPath {
				return ErrEgress
			}
			state = 2
		case 2:
			if line != "+++ b/"+curPath {
				return ErrEgress
			}
			state = 3
		case 3:
			if !strings.HasPrefix(line, "@@ ") {
				return ErrEgress
			}
			state = 4
		case 4:
			if !validHunkLine(line) {
				return ErrEgress
			}
		}
	}

	if state < 4 {
		return ErrEgress
	}
	covered[curPath] = true

	for f := range allowed {
		if !covered[f] {
			return ErrEgress
		}
	}
	return nil
}

// parseHeaderPath extracts and validates the path from a diff --git header.
func parseHeaderPath(line string) (string, error) {
	rest := line[len("diff --git "):]
	if !strings.HasPrefix(rest, "a/") {
		return "", ErrEgress
	}
	idx := strings.Index(rest[2:], " b/")
	if idx < 0 {
		return "", ErrEgress
	}
	pA := rest[2 : 2+idx]
	pB := rest[2+idx+3:]
	if pA != pB || pA == "" {
		return "", ErrEgress
	}
	if strings.ContainsRune(pA, '"') {
		return "", ErrEgress
	}
	return pA, nil
}

// validHunkLine reports whether line is acceptable inside a diff hunk body.
func validHunkLine(line string) bool {
	if strings.HasPrefix(line, " ") ||
		strings.HasPrefix(line, "+") ||
		strings.HasPrefix(line, "-") ||
		strings.HasPrefix(line, "@@ ") {
		return true
	}
	if line == "\\ No newline at end of file" {
		return true
	}
	return false
}
