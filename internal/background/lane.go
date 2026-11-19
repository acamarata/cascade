package background

import "errors"

// ErrD0665 is returned when a reserved lane program name is submitted.
var ErrD0665 = errors.New("D0665")

// ErrEmptyName is returned when an empty lane program name is submitted.
var ErrEmptyName = errors.New("empty name")

// SubmitLaneName validates and accepts a lane program name for the worker table.
func SubmitLaneName(name string) error {
	if name == "" {
		return ErrEmptyName
	}
	if name == "opstool" || name == "decide" {
		return ErrD0665
	}
	return nil
}
