// Package background provides scheduling primitives for cascade.
package background

import (
	"errors"
	"strings"
)

// Trigger represents the origin of a schedule request.
type Trigger int

const (
	// TriggerAuto indicates an automatic trigger.
	TriggerAuto Trigger = iota
	// TriggerManual indicates a manual trigger.
	TriggerManual
	// TriggerQueueAdd indicates a queue-add trigger.
	TriggerQueueAdd
)

// LiveCountSource provides the current live count.
type LiveCountSource interface {
	// LiveCount returns the number of live items and any error.
	LiveCount() (int, error)
}

// BackgroundEnqueuer enqueues work by identifier.
type BackgroundEnqueuer interface { //nolint:revive // name fixed by the ticket contract
	// Enqueue adds the item identified by id to the background queue.
	Enqueue(id string) error
}

// ScheduleRequest holds the parameters for a Schedule call.
type ScheduleRequest struct {
	// ID is the identifier of the item to schedule.
	ID string
	// Trigger is the origin of the request.
	Trigger Trigger
	// MaxLive is the maximum allowed live count for non-manual triggers.
	MaxLive int
}

// ErrInvalidSchedule is returned when validation of the schedule request fails.
var ErrInvalidSchedule = errors.New("invalid schedule")

// ErrLiveCountLimit is returned when the live count exceeds the allowed maximum.
var ErrLiveCountLimit = errors.New("live count limit reached")

// Schedule validates req and, for manual triggers, enqueues the item directly.
// It returns true on success or false with an error describing the failure.
func Schedule(req ScheduleRequest, _ LiveCountSource, queue BackgroundEnqueuer) (bool, error) {
	if strings.TrimSpace(req.ID) == "" {
		return false, ErrInvalidSchedule
	}
	if req.Trigger != TriggerAuto && req.Trigger != TriggerManual && req.Trigger != TriggerQueueAdd {
		return false, ErrInvalidSchedule
	}
	if queue == nil {
		return false, ErrInvalidSchedule
	}

	if req.Trigger == TriggerManual {
		err := queue.Enqueue(req.ID)
		if err != nil {
			return false, err
		}
		return true, nil
	}

	if req.Trigger == TriggerQueueAdd {
		return scheduleQueueAdd(req, queue)
	}

	return false, ErrInvalidSchedule
}
