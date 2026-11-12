// Package read provides control plane read operations.
package read

import (
	"errors"
	"strings"
	"time"
)

// Blocker represents a ticket blocker.
type Blocker struct {
	Ticket    string
	Kind      string
	Ref       string
	CreatedAt time.Time
	Cleared   bool
}

// Event represents a ticket event.
type Event struct {
	ID   string
	Kind string
	At   time.Time
}

// TicketRecord represents the stored record of a ticket.
type TicketRecord struct {
	State          string
	Stage          string
	Level          string
	AIInstructions string
	Owner          string
	Blockers       []Blocker
	Dependencies   []string
	Attempts       int
	Events         []Event
	StateEnteredAt time.Time
}

// Store defines the interface for ticket retrieval.
type Store interface {
	Ticket(id string) (TicketRecord, bool, error)
}

// LiveView represents the live view of a ticket.
type LiveView struct {
	State          string
	Stage          string
	Level          string
	AIInstructions string
	Owner          string
	Blockers       []Blocker
	Dependencies   []string
	Attempts       int
	Events         []Event
	TimeInState    time.Duration
}

// ErrUnknownTicket is returned when a ticket is not found in the store.
var ErrUnknownTicket = errors.New("D4608")

// ErrInvalidLiveView is returned when the input parameters or record are invalid.
var ErrInvalidLiveView = errors.New("invalid live ticket view")

// GetLiveView retrieves and projects a LiveView for the given ticket ID.
func GetLiveView(s Store, id string, now time.Time) (*LiveView, error) {
	if s == nil {
		return nil, ErrInvalidLiveView
	}
	if strings.TrimSpace(id) == "" {
		return nil, ErrInvalidLiveView
	}
	if now.IsZero() {
		return nil, ErrInvalidLiveView
	}

	rec, found, err := s.Ticket(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrUnknownTicket
	}

	if rec.Attempts < 0 {
		return nil, ErrInvalidLiveView
	}
	if !rec.StateEnteredAt.IsZero() && rec.StateEnteredAt.After(now) {
		return nil, ErrInvalidLiveView
	}

	var timeInState time.Duration
	if !rec.StateEnteredAt.IsZero() {
		timeInState = now.Sub(rec.StateEnteredAt)
	}

	blockers := make([]Blocker, len(rec.Blockers))
	copy(blockers, rec.Blockers)

	dependencies := make([]string, len(rec.Dependencies))
	copy(dependencies, rec.Dependencies)

	events := make([]Event, len(rec.Events))
	copy(events, rec.Events)

	return &LiveView{
		State:          rec.State,
		Stage:          rec.Stage,
		Level:          rec.Level,
		AIInstructions: rec.AIInstructions,
		Owner:          rec.Owner,
		Blockers:       blockers,
		Dependencies:   dependencies,
		Attempts:       rec.Attempts,
		Events:         events,
		TimeInState:    timeInState,
	}, nil
}
