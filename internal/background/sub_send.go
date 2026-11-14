// Package background provides subscription-based job sending.
package background

import (
	"errors"
	"strings"
)

// Error represents a domain error with a code and reason.
type Error struct {
	// Code is the error category identifier.
	Code string
	// Reason describes the specific failure.
	Reason string
}

// Error returns the formatted error string.
func (e Error) Error() string {
	return e.Code + ": " + e.Reason
}

// Is reports whether target matches this error by code and reason.
func (e Error) Is(target error) bool {
	var t Error
	if !errors.As(target, &t) {
		return false
	}
	return e.Code == t.Code && e.Reason == t.Reason
}

// ErrInvalidChat is returned when a chat ID is blank or whitespace-only.
var ErrInvalidChat = Error{Code: "D6100", Reason: "invalid chat"}

// ErrDuplicateChat is returned when registering an already-registered chat ID.
var ErrDuplicateChat = Error{Code: "D6100", Reason: "duplicate chat"}

// ErrEmptyJob is returned when an empty job is submitted.
var ErrEmptyJob = Error{Code: "D6100", Reason: "empty job"}

// ErrUnknownChat is returned when referencing an unregistered chat ID.
var ErrUnknownChat = Error{Code: "D6100", Reason: "unknown chat"}

// SubSend manages per-chat contexts and job queues.
// The zero value is ready to use.
type SubSend struct {
	ctxs   map[string]string
	queues map[string][]string
}

// NewSubSend creates and returns a new initialized SubSend.
func NewSubSend() *SubSend {
	return &SubSend{
		ctxs:   make(map[string]string),
		queues: make(map[string][]string),
	}
}

// Register associates a context string with a chat ID and initializes its queue.
// It returns ErrInvalidChat for nil receiver or blank/whitespace-only IDs,
// and ErrDuplicateChat if the ID is already registered.
func (s *SubSend) Register(chatID, ctx string) error {
	if s == nil || strings.TrimSpace(chatID) == "" {
		return ErrInvalidChat
	}
	if s.ctxs == nil {
		s.ctxs = make(map[string]string)
	}
	if s.queues == nil {
		s.queues = make(map[string][]string)
	}
	if _, exists := s.ctxs[chatID]; exists {
		return ErrDuplicateChat
	}
	s.ctxs[chatID] = ctx
	s.queues[chatID] = nil
	return nil
}

// Context returns the context string for the given chat ID.
// It returns an empty string for unknown chat IDs or nil receivers.
func (s *SubSend) Context(chatID string) string {
	if s == nil || s.ctxs == nil {
		return ""
	}
	return s.ctxs[chatID]
}

// Send appends job verbatim to the chat's queue.
// Send on nil/unknown/blank chat returns unknown-chat Error;
// Send on a known chat with blank/whitespace-only job returns empty-job Error.
// Validation never mutates queues/context.
func (s *SubSend) Send(chatID, job string) error {
	if s == nil || s.ctxs == nil {
		return ErrUnknownChat
	}
	if strings.TrimSpace(chatID) == "" {
		return ErrUnknownChat
	}
	if _, exists := s.ctxs[chatID]; !exists {
		return ErrUnknownChat
	}
	if strings.TrimSpace(job) == "" {
		return ErrEmptyJob
	}
	if s.queues == nil {
		s.queues = make(map[string][]string)
	}
	s.queues[chatID] = append(s.queues[chatID], job)
	return nil
}

// Run returns and clears the job queue for the given chat ID.
// It returns a non-nil empty slice if the queue is empty or was just cleared.
// It returns nil for unknown chat IDs or nil receivers.
func (s *SubSend) Run(chatID string) []string {
	if s == nil || s.queues == nil {
		return nil
	}
	q, exists := s.queues[chatID]
	if !exists {
		return nil
	}
	s.queues[chatID] = nil
	if q == nil {
		return []string{}
	}
	return q
}
