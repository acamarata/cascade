// Package mod provides module and plugin registration utilities.
package mod

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalid is returned when a group or verb is empty.
	ErrInvalid = errors.New("invalid: group and verb must not be empty")
	// ErrGroupNotAllowed is returned when a group is not in the allowed list.
	ErrGroupNotAllowed = errors.New("group not allowed")
	// ErrFleetRetired is returned when the retired 'fleet' group is used.
	ErrFleetRetired = errors.New("fleet group is retired")
	// ErrCoreVerb is returned when the 'core' group is used to register a plugin.
	ErrCoreVerb = errors.New("core verbs cannot be registered through this path")
)

// allowedGroups defines the set of groups permitted for plugin registration.
var allowedGroups = map[string]bool{
	"ci":     true,
	"ai":     true,
	"nodes":  true,
	"jobs":   true,
	"events": true,
	"store":  true,
	"guard":  true,
	"tools":  true,
}

// RegisterPlugin registers a plugin command for the given group and verb.
// It returns an error if the group or verb is invalid, if the group is retired,
// if the group is restricted, or if the group is not in the allowed list.
func RegisterPlugin(group, verb string) error {
	if group == "" || verb == "" {
		return ErrInvalid
	}
	if group == "fleet" {
		return ErrFleetRetired
	}
	if group == "core" {
		return ErrCoreVerb
	}
	if !allowedGroups[group] {
		return fmt.Errorf("%w: %s", ErrGroupNotAllowed, group)
	}
	return nil
}
