// Package background provides egress diff validation for background operations.
package background

import (
	"strings"
	"unicode/utf8"
)

// scanDiff validates a unified diff string against the allowed file set.
// It scans line-by-line in O(len(diff)) without regex backtracking.
func scanDiff(diff string, allowed map[string]bool) error {
	if diff == "" || !utf8.ValidString(diff) {
		return ErrEgress
	}

	lines := strings.Split(diff, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ErrEgress
	}

	covered := make(map[string]bool, len(allowed))
	state := 0
	var curPath string

	for _, line := range lines {
		var err error
		state, curPath, err = processDiffLine(line, state, curPath, covered, allowed)
		if err != nil {
			return err
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

func processDiffLine(line string, state int, curPath string, covered map[string]bool, allowed map[string]bool) (int, string, error) {
	if strings.ContainsRune(line, '\x00') || strings.ContainsRune(line, '\r') {
		return 0, "", ErrEgress
	}
	if strings.HasPrefix(line, "diff --cc ") ||
		strings.HasPrefix(line, "diff --combined ") ||
		strings.HasPrefix(line, "@@@") ||
		strings.HasPrefix(line, "GIT binary patch") ||
		strings.HasPrefix(line, "Binary files ") {
		return 0, "", ErrEgress
	}

	if strings.HasPrefix(line, "diff --git ") {
		return handleGitHeader(line, state, curPath, covered, allowed)
	}

	newState, err := handleStateLine(line, state, curPath)
	return newState, curPath, err
}

func handleGitHeader(line string, state int, curPath string, covered map[string]bool, allowed map[string]bool) (int, string, error) {
	if state > 0 && state < 4 {
		return 0, "", ErrEgress
	}
	if state == 4 {
		covered[curPath] = true
	}
	p, err := parseHeaderPath(line)
	if err != nil {
		return 0, "", err
	}
	if covered[p] || !allowed[p] {
		return 0, "", ErrEgress
	}
	return 1, p, nil
}

func handleStateLine(line string, state int, curPath string) (int, error) {
	switch state {
	case 0:
		return 0, ErrEgress
	case 1:
		if line != "--- a/"+curPath {
			return 0, ErrEgress
		}
		return 2, nil
	case 2:
		if line != "+++ b/"+curPath {
			return 0, ErrEgress
		}
		return 3, nil
	case 3:
		if !strings.HasPrefix(line, "@@ ") {
			return 0, ErrEgress
		}
		return 4, nil
	case 4:
		if !validHunkLine(line) {
			return 0, ErrEgress
		}
		return 4, nil
	default:
		return 0, ErrEgress
	}
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
