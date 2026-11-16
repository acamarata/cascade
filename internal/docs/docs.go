// Package docs provides document storage and validation utilities.
package docs

import (
	"fmt"
	"sort"
	"strings"
)

// Doc represents a document with a name, content, and optional aliases.
type Doc struct {
	Name    string
	Content string
	Aliases []string
}

// Store holds a collection of documents.
type Store struct {
	Docs []Doc
}

// Error represents an error with a code and message.
type Error struct {
	Code int
	Msg  string
}

// Error returns the formatted error string.
func (e *Error) Error() string {
	return fmt.Sprintf("%d: %s", e.Code, e.Msg)
}

// ErrDocNotFound is returned when a document is not found.
var ErrDocNotFound = &Error{404, "not found"}

// ErrInvalidDocInput is returned when document input is invalid.
var ErrInvalidDocInput = &Error{400, "invalid doc input"}

// isValidName checks if a name is valid: nonempty, trimmed, without NUL/CR/LF.
func isValidName(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return false
	}
	for _, c := range s {
		if c == '\x00' || c == '\r' || c == '\n' {
			return false
		}
	}
	return true
}

// isValidAlias checks if an alias is valid: nonempty, trimmed, without NUL/CR/LF.
func isValidAlias(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return false
	}
	for _, c := range s {
		if c == '\x00' || c == '\r' || c == '\n' {
			return false
		}
	}
	return true
}

// isEmptyContent checks if content is whitespace-only.
func isEmptyContent(s string) bool {
	return len(s) > 0 && strings.TrimSpace(s) == ""
}

// Lint validates the store and returns sorted violation messages.
// It uses a value receiver and does not modify the store.
func (s Store) Lint() []string {
	violations := make(map[string]bool)
	validNames := make(map[string]bool)
	allValidAliases := make(map[string]bool)

	for i, doc := range s.Docs {
		name := doc.Name
		validName := isValidName(name)

		if !validName {
			violations[fmt.Sprintf("doc[%d]: invalid name", i)] = true
		} else {
			if validNames[name] {
				violations[fmt.Sprintf("%s: duplicate name", name)] = true
			} else {
				validNames[name] = true
			}
		}

		if isEmptyContent(doc.Content) {
			violations[fmt.Sprintf("doc[%d]: empty content", i)] = true
		}

		for _, alias := range doc.Aliases {
			if !isValidAlias(alias) {
				violations[fmt.Sprintf("doc[%d]: invalid alias", i)] = true
			} else {
				if allValidAliases[alias] || validNames[alias] {
					violations[fmt.Sprintf("%s: duplicate alias", alias)] = true
				} else {
					allValidAliases[alias] = true
				}
			}
		}
	}

	var result []string
	for k := range violations {
		result = append(result, k)
	}
	sort.Strings(result)

	if result == nil {
		return []string{}
	}
	return result
}
