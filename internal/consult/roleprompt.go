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

	body := string(data)
	body = strings.TrimPrefix(body, "\xef\xbb\xbf")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.TrimSpace(body)

	if body == "" || utf8.RuneCountInString(body) > 5000 {
		return fallback
	}
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
