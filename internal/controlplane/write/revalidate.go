// Package write provides revalidation logic for the control plane.
package write

import (
	"errors"
	"strings"
)

// ErrOwnerOnly indicates an operation restricted to owners.
var ErrOwnerOnly = errors.New("owner only")

// ErrInvalidRevalidate indicates the revalidate request failed validation.
var ErrInvalidRevalidate = errors.New("invalid revalidate request")

// Item represents a single entity to be revalidated.
type Item struct {
	ID string
}

// Parker defines the interface for parking items.
type Parker interface {
	Park(item Item) error
}

// Notifier defines the interface for notifying about items.
type Notifier interface {
	Notify(item Item) error
}

// Request holds all parameters needed for a Revalidate call.
type Request struct {
	Mode     string
	Caller   string
	Stale    []Item
	Parker   Parker
	Notifier Notifier
}

// RevalidateResult holds the counts of successful operations.
type RevalidateResult struct {
	Notices int
	Parked  int
}

// Revalidate processes stale items according to the requested mode.
func Revalidate(req Request) (RevalidateResult, error) {
	mode := req.Mode
	if mode == "" {
		mode = "park"
	}

	switch mode {
	case "park":
		if strings.TrimSpace(req.Caller) == "" {
			return RevalidateResult{}, ErrInvalidRevalidate
		}
	case "notify_only":
		if req.Caller != "owner" {
			return RevalidateResult{}, ErrInvalidRevalidate
		}
	default:
		return RevalidateResult{}, ErrInvalidRevalidate
	}

	if err := validateStaleItems(req.Stale); err != nil {
		return RevalidateResult{}, err
	}
	if len(req.Stale) == 0 {
		return RevalidateResult{}, nil
	}
	if mode == "park" {
		return parkAll(req)
	}
	return notifyAll(req)
}

func validateStaleItems(items []Item) error {
	seen := make(map[string]struct{})
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" || item.ID != strings.TrimSpace(item.ID) {
			return ErrInvalidRevalidate
		}
		if _, exists := seen[item.ID]; exists {
			return ErrInvalidRevalidate
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

func parkAll(req Request) (RevalidateResult, error) {
	if req.Parker == nil {
		return RevalidateResult{}, ErrInvalidRevalidate
	}
	var parked int
	for _, item := range req.Stale {
		if err := req.Parker.Park(item); err != nil {
			return RevalidateResult{Parked: parked}, err
		}
		parked++
	}
	return RevalidateResult{Parked: parked}, nil
}

func notifyAll(req Request) (RevalidateResult, error) {
	if req.Notifier == nil {
		return RevalidateResult{}, ErrInvalidRevalidate
	}
	var notices int
	for _, item := range req.Stale {
		if err := req.Notifier.Notify(item); err != nil {
			return RevalidateResult{Notices: notices}, err
		}
		notices++
	}
	return RevalidateResult{Notices: notices}, nil
}
