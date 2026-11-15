// Package lifecycle provides types and functions for managing application
// lifecycle phases, command parsing, and wait duration handling.
package lifecycle

import "errors"

// ErrNoCommands is returned when no commands are found in the input.
var ErrNoCommands = errors.New("no commands found")

// ErrInvalidWait is returned when a wait duration cannot be parsed.
var ErrInvalidWait = errors.New("invalid wait duration")

// ParseCommands parses a command string and returns a slice of individual
// commands. It returns ErrNoCommands if the input contains no valid commands.
func ParseCommands(_ string) ([]string, error) {
	return nil, ErrNoCommands
}

// ParseWait parses a wait duration string and returns the duration as an
// integer number of seconds. It returns ErrInvalidWait if the input cannot
// be parsed as a valid wait duration.
func ParseWait(_ string) (int, error) {
	return 0, ErrInvalidWait
}
