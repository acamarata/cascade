// Package log provides garbage-collection logging utilities.
package log

import (
	"errors"
	"log"
)

// ErrNoStore is returned when a sink has no backing store.
var ErrNoStore = errors.New("no store configured")

// Event represents a single log event to be recorded by a Sink.
type Event struct {
	// Message is the textual content of the event.
	Message string
}

// Store persists events. A nil Store means no persistence is available.
type Store interface {
	// Save persists the given event.
	Save(e Event) error
}

// Logger abstracts log output so that tests can inject a recorder.
type Logger interface {
	// Printf logs a formatted message.
	Printf(format string, args ...any)
}

// stdLogger wraps the standard library logger to satisfy Logger.
type stdLogger struct{}

// Printf delegates to log.Printf.
func (stdLogger) Printf(format string, args ...any) {
	log.Printf(format, args...)
}

// Sink collects events and forwards them to a Store.
type Sink struct {
	store      Store
	logger     Logger
	failures   int
	loggedOnce bool
}

// NewSink creates a Sink with the given store and logger.
// If logger is nil, the standard library logger is used.
func NewSink(store Store, logger Logger) *Sink {
	if logger == nil {
		logger = stdLogger{}
	}
	return &Sink{
		store:  store,
		logger: logger,
	}
}

// Record processes an event. When the store is nil every event increments
// the failure counter with ErrNoStore, but only the first occurrence is logged.
func (s *Sink) Record(e Event) {
	if s.store == nil {
		s.failures++
		if !s.loggedOnce {
			s.logger.Printf("sink failure: %v", ErrNoStore)
			s.loggedOnce = true
		}
		return
	}
	_ = s.store.Save(e)
}

// Failures returns the number of events that could not be stored.
func (s *Sink) Failures() int {
	return s.failures
}
