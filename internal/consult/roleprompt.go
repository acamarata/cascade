// Package consult provides role-based prompt loading utilities.
package consult

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// LoadRolePrompt returns the prompt text for the given role.
// It looks up a file named role+".md" under dir, validates the path,
// and falls back to built-in prompts when the file is missing or invalid.
func LoadRolePrompt(dir, role string) string {
	fallback := builtinFallback(role)
	if !validRole(role) || dir == "" {
		return fallback
	}
	resolvedDir, err := resolveDir(dir)
	if err != nil || !dirHasRole(resolvedDir, role) {
		return fallback
	}
	data, ok := readRoleFile(resolvedDir, filepath.Join(dir, role+".md"))
	if !ok {
		return fallback
	}

	if !utf8.Valid(data) {
		return fallback
	}

	body := string(data)
	body = strings.TrimPrefix(body, "\xef\xbb\xbf")
	body = strings.ReplaceAll(body, "\r\n", "\n")

	body = stripFrontMatter(body)
	body = strings.TrimSpace(body)

	if body == "" {
		return fallback
	}

	body = truncateRunes(body, 5000)

	return body
}

func resolveDir(dir string) (string, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func dirHasRole(resolvedDir, role string) bool {
	entries, err := os.ReadDir(resolvedDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() == role+".md" {
			return true
		}
	}
	return false
}

func readRoleFile(resolvedDir, candidate string) ([]byte, bool) {
	resolvedCandidate, err := resolveDir(candidate)
	if err != nil {
		return nil, false
	}
	rel, err := filepath.Rel(resolvedDir, resolvedCandidate)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, false
	}
	info, err := os.Stat(resolvedCandidate)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	data, err := os.ReadFile(resolvedCandidate)
	if err != nil {
		return nil, false
	}
	return data, true
}

// stripFrontMatter removes a leading YAML-style front matter block delimited
// by lines containing exactly "---". If the opening delimiter is present but
// no closing delimiter follows, the entire content is discarded (returns "").
func stripFrontMatter(s string) string {
	if !strings.HasPrefix(s, "---\n") && s != "---" {
		return s
	}
	rest := s[4:]
	idx := strings.Index(rest, "\n---\n")
	if idx >= 0 {
		return rest[idx+5:]
	}
	if strings.HasSuffix(rest, "\n---") {
		return rest[:len(rest)-4]
	}
	if rest == "---" {
		return ""
	}
	return ""
}

// truncateRunes returns the first maxRunes Unicode code points of s.
func truncateRunes(s string, maxRunes int) string {
	count := utf8.RuneCountInString(s)
	if count <= maxRunes {
		return s
	}
	byteIdx := 0
	for i := 0; i < maxRunes; i++ {
		_, size := utf8.DecodeRuneInString(s[byteIdx:])
		byteIdx += size
	}
	return s[:byteIdx]
}

func validRole(role string) bool {
	if role == "" || role == "." || role == ".." {
		return false
	}
	if strings.ContainsAny(role, "/\\") {
		return false
	}
	return true
}

func builtinFallback(role string) string {
	switch role {
	case "Critic":
		return "Identify flaws and risks."
	case "Judge":
		return "Evaluate the evidence and give a verdict."
	default:
		return "Provide a concise, accurate response."
	}
}
