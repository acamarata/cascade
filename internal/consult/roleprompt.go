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
	if !validRole(role) {
		return fallback
	}
	if dir == "" {
		return fallback
	}

	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fallback
	}
	resolvedDir, err = filepath.Abs(resolvedDir)
	if err != nil {
		return fallback
	}

	entries, err := os.ReadDir(resolvedDir)
	if err != nil {
		return fallback
	}
	found := false
	for _, e := range entries {
		if e.Name() == role+".md" {
			found = true
			break
		}
	}
	if !found {
		return fallback
	}

	candidate := filepath.Join(dir, role+".md")
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return fallback
	}
	resolvedCandidate, err = filepath.Abs(resolvedCandidate)
	if err != nil {
		return fallback
	}

	rel, err := filepath.Rel(resolvedDir, resolvedCandidate)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fallback
	}

	info, err := os.Stat(resolvedCandidate)
	if err != nil || !info.Mode().IsRegular() {
		return fallback
	}

	data, err := os.ReadFile(resolvedCandidate)
	if err != nil {
		return fallback
	}

	body := string(data)
	body = strings.TrimPrefix(body, "\xef\xbb\xbf")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.TrimSpace(body)

	if body == "" {
		return fallback
	}

	if utf8.RuneCountInString(body) > 5000 {
		return fallback
	}

	return body
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
