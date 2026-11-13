// Package mod provides hook pack registration and execution.
package mod

import (
	"errors"
	"fmt"
)

// ErrBadHook is returned when a hook registration is invalid.
var ErrBadHook = errors.New("bad hook")

// HookPack manages a registry of hook functions.
type HookPack struct {
	hooks []namedHook
	names map[string]struct{}
}

type namedHook struct {
	name string
	fn   func() error
}

// NewHookPack creates a new HookPack instance.
func NewHookPack() *HookPack {
	return &HookPack{
		names: make(map[string]struct{}),
	}
}

// Register adds a hook to the pack. It rejects empty or duplicate names with ErrBadHook.
func (h *HookPack) Register(name string, fn func() error) error {
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrBadHook)
	}
	if _, ok := h.names[name]; ok {
		return fmt.Errorf("%w: duplicate name %q", ErrBadHook, name)
	}
	h.names[name] = struct{}{}
	h.hooks = append(h.hooks, namedHook{name: name, fn: fn})
	return nil
}

// RunAll executes registered hooks in order. It returns the first error wrapped with the hook name.
func (h *HookPack) RunAll() error {
	for _, hook := range h.hooks {
		if err := hook.fn(); err != nil {
			return fmt.Errorf("hook %s: %w", hook.name, err)
		}
	}
	return nil
}

// PlatformTargets returns the list of supported platform targets.
func PlatformTargets() []string {
	return []string{
		"darwin/arm64",
		"darwin/amd64",
		"linux/amd64",
		"linux/arm64",
	}
}
