// Package observe provides run-end observation hooks.
package observe

import (
	"errors"
	"reflect"
	"strings"
	"time"
)

// ErrInvalidRunEndInput is returned when input validation fails.
var ErrInvalidRunEndInput = errors.New("onrunend: invalid input")

// ErrDuplicateRun is returned when a run ID has already been recorded.
var ErrDuplicateRun = errors.New("onrunend: duplicate run")

// Run represents a completed run.
type Run struct {
	// ID is the unique identifier for the run.
	ID string
	// Kind is the type of run, either "code" or "chat".
	Kind string
	// Private indicates whether the run contains private data.
	Private bool
	// EndedAt is the time the run ended.
	EndedAt time.Time
}

// Rec represents a recommendation record.
type Rec struct {
	// RunID is the identifier of the originating run.
	RunID string
	// Domain is the domain of the recommendation.
	Domain string
	// Text is the recommendation content.
	Text string
}

// RunEndStore persists run and recommendation state.
type RunEndStore interface {
	// HasRun reports whether the given run ID already exists.
	HasRun(id string) (bool, error)
	// HasRecommendation reports whether a recommendation exists for the day.
	HasRecommendation(day string) (bool, error)
	// Commit atomically records the run and optional recommendation.
	Commit(run Run, day string, rec Rec, hasRec bool) error
}

// DomainDecider decides on recommendations for a run and domain.
type DomainDecider interface {
	// Decide returns a recommendation and whether one was produced.
	Decide(run Run, domain string) (Rec, bool, error)
}

// RecommendationInbox delivers recommendations.
type RecommendationInbox interface {
	// Notify sends a recommendation to the inbox.
	Notify(rec Rec) error
}

// EndHook processes run-end events.
type EndHook struct {
	// Store persists run data.
	Store RunEndStore
	// Decider produces recommendations.
	Decider DomainDecider
	// Inbox delivers recommendations.
	Inbox RecommendationInbox
}

// OnRunEnd validates and records a completed run.
func (h EndHook) OnRunEnd(run Run, domains []string) error {
	if err := validateRun(run); err != nil {
		return err
	}
	if err := validateDeps(h); err != nil {
		return err
	}
	if err := validateDomains(domains); err != nil {
		return err
	}

	if run.Kind == "chat" || run.Private {
		return nil
	}

	has, err := h.Store.HasRun(run.ID)
	if err != nil {
		return err
	}
	if has {
		return ErrDuplicateRun
	}

	day := run.EndedAt.Format("2006-01-02")

	if len(domains) == 0 {
		return h.Store.Commit(run, day, Rec{}, false)
	}

	for _, d := range domains {
		rec, ok, err := h.Decider.Decide(run, d)
		if err != nil {
			return err
		}
		if ok {
			if err := h.Inbox.Notify(rec); err != nil {
				return err
			}
			return h.Store.Commit(run, day, rec, true)
		}
	}

	return h.Store.Commit(run, day, Rec{}, false)
}

func validateRun(run Run) error {
	if run.Kind != "code" && run.Kind != "chat" {
		return ErrInvalidRunEndInput
	}
	if strings.TrimSpace(run.ID) == "" || run.ID != strings.TrimSpace(run.ID) {
		return ErrInvalidRunEndInput
	}
	for _, r := range run.ID {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return ErrInvalidRunEndInput
		}
	}
	if run.EndedAt.IsZero() {
		return ErrInvalidRunEndInput
	}
	return nil
}

func validateDeps(h EndHook) error {
	if isNil(h.Store) || isNil(h.Decider) || isNil(h.Inbox) {
		return ErrInvalidRunEndInput
	}
	return nil
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Interface, reflect.Slice:
		return rv.IsNil()
	case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.Array,
		reflect.String, reflect.Struct, reflect.UnsafePointer:
		return false
	default:
		return false
	}
}

func validateDomains(domains []string) error {
	if len(domains) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(domains))
	for _, d := range domains {
		if strings.TrimSpace(d) == "" || d != strings.TrimSpace(d) {
			return ErrInvalidRunEndInput
		}
		for _, r := range d {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				return ErrInvalidRunEndInput
			}
		}
		if seen[d] {
			return ErrInvalidRunEndInput
		}
		seen[d] = true
	}
	return nil
}
