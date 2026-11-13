package consult

import (
	"errors"
	"unicode"
)

// ErrBadModelName indicates that a model name is invalid.
var ErrBadModelName = errors.New("bad model name")

// ValidateModelName returns nil if name is a valid model name,
// or ErrBadModelName otherwise. A valid name is non-empty, entirely lowercase,
// contains no '.' between two letters, and has no run of more than 8 identical characters.
func ValidateModelName(name string) error {
	if name == "" {
		return ErrBadModelName
	}

	runes := []rune(name)
	for _, r := range runes {
		if unicode.IsUpper(r) {
			return ErrBadModelName
		}
	}

	for i := 1; i < len(runes)-1; i++ {
		if runes[i] == '.' {
			if unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1]) {
				return ErrBadModelName
			}
		}
	}

	run := 1
	for i := 1; i < len(runes); i++ {
		if runes[i] == runes[i-1] {
			run++
			if run > 8 {
				return ErrBadModelName
			}
		} else {
			run = 1
		}
	}

	return nil
}
