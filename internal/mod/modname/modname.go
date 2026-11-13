// Package modname validates module names against the naming rules and the module map.
package modname

import (
	"errors"
	"strings"
	"unicode"
)

var (
	// ErrNotOneWord indicates the module name is not a single word.
	ErrNotOneWord = errors.New("module name must be a single word")
	// ErrNotCapitalized indicates the module name does not start with an uppercase letter
	// or contains characters other than letters and digits.
	ErrNotCapitalized = errors.New("module name must be capitalized and contain only letters and digits")
	// ErrDuplicate indicates the module name already exists.
	ErrDuplicate = errors.New("module name already exists")
	// ErrNoMapRow indicates the module name has no matching map row.
	ErrNoMapRow = errors.New("module name has no matching map row")
)

// Validate enforces that a module name is a single word, starts with an uppercase letter
// followed only by letters and digits, is not in the existing names, and has a matching map row.
func Validate(name string, existing []string, mapRows []string) error {
	if len(strings.Fields(name)) != 1 {
		return ErrNotOneWord
	}

	runes := []rune(name)
	if !unicode.IsUpper(runes[0]) {
		return ErrNotCapitalized
	}
	for _, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return ErrNotCapitalized
		}
	}

	for _, e := range existing {
		if e == name {
			return ErrDuplicate
		}
	}

	for _, m := range mapRows {
		if m == name {
			return nil
		}
	}

	return ErrNoMapRow
}
