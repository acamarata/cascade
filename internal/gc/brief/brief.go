// Package brief defines the Brief type and validation rules for garbage collection briefs.
package brief

import (
	"errors"
	"path"
)

// ErrTooLarge is returned when a brief is 8192 bytes or larger.
var ErrTooLarge = errors.New("brief too large")

// ErrTooManyScope is returned when a brief has more than 6 scope entries.
var ErrTooManyScope = errors.New("too many scope entries")

// ErrMultiPackage is returned when a loop brief names files in more than one package directory.
var ErrMultiPackage = errors.New("multiple packages")

// ErrNoFileList is returned when a loop brief has an empty file list.
var ErrNoFileList = errors.New("no file list")

// Brief holds the text, scope and file list for a gc brief.
type Brief struct {
	Text  string
	Scope []string
	Files []string
}

// Validate checks the size, scope count and single-package constraints of a brief.
func Validate(kind string, b Brief) error {
	if len(b.Text) >= 8192 {
		return ErrTooLarge
	}
	if len(b.Scope) > 6 {
		return ErrTooManyScope
	}

	if kind == "loop" {
		if len(b.Files) == 0 {
			return ErrNoFileList
		}
		var dir string
		for _, f := range b.Files {
			d := path.Dir(f)
			if dir == "" {
				dir = d
			} else if d != dir {
				return ErrMultiPackage
			}
		}
	}
	return nil
}
