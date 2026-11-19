package background

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Payload represents the egress request data to be validated.
type Payload struct {
	Repo   string
	Files  []string
	Diff   string
	Passes int
}

// ErrEgress is returned when an egress check fails.
var ErrEgress = errors.New("D4354: egress rejected")

// protectedComponents lists directory or file components that are never allowed.
var protectedComponents = map[string]bool{
	".git":     true,
	".claude":  true,
	".codex":   true,
	".ai":      true,
	".ssh":     true,
	".aws":     true,
	"secrets":  true,
	"keychain": true,
}

// CheckEgress validates a Payload against strict local defaults.
// It returns nil only for allowed input; every rejection returns ErrEgress.
func CheckEgress(p Payload) error {
	if p.Repo != "exact-fake/dev-repo" {
		return ErrEgress
	}

	if len(p.Files) == 0 {
		return ErrEgress
	}

	seen := make(map[string]bool, len(p.Files))
	for _, f := range p.Files {
		if f == "" {
			return ErrEgress
		}
		if seen[f] {
			return ErrEgress
		}
		seen[f] = true

		if err := validatePath(f); err != nil {
			return err
		}
	}

	if err := validateDiff(p.Diff, seen); err != nil {
		return err
	}

	return nil
}

// validatePath checks a single repository-relative path for safety.
func validatePath(p string) error {
	if !utf8.ValidString(p) {
		return ErrEgress
	}

	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return ErrEgress
	}

	for i := 0; i < len(p); i++ {
		b := p[i]
		if b < 0x20 || b == 0x7f {
			return ErrEgress
		}
	}

	parts := strings.Split(p, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ErrEgress
		}
		if protectedComponents[part] {
			return ErrEgress
		}
	}

	base := parts[len(parts)-1]
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return ErrEgress
	}

	lowerBase := strings.ToLower(base)
	if strings.HasSuffix(lowerBase, ".pem") ||
		strings.HasSuffix(lowerBase, ".key") ||
		strings.HasSuffix(lowerBase, ".p12") ||
		strings.HasSuffix(lowerBase, ".pfx") {
		return ErrEgress
	}

	return nil
}

// validateDiff delegates to scanDiff for strict linear diff validation.
func validateDiff(diff string, allowed map[string]bool) error {
	return scanDiff(diff, allowed)
}
