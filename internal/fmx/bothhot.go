package fmx

import "time"

// Clock provides the current time.
type Clock interface {
	Now() time.Time
}

// UsageLog logs usage rows.
type UsageLog interface {
	Append(row string)
}

// Tracker tracks both-hot episodes and manages budget.
type Tracker struct {
	clock     Clock
	log       UsageLog
	normal    int
	lowered   int
	inEpisode bool
	lastHot   time.Time
}

// NewTracker creates a new Tracker with the given clock, log, and budget values.
func NewTracker(c Clock, l UsageLog, normal, lowered int) *Tracker {
	return &Tracker{
		clock:   c,
		log:     l,
		normal:  normal,
		lowered: lowered,
	}
}

// Tick processes a tick and returns the current budget and whether an episode ended.
func (t *Tracker) Tick(bothHot bool) (int, bool) {
	now := t.clock.Now()
	ended := false

	if bothHot {
		if !t.inEpisode {
			t.inEpisode = true
			t.lastHot = now
			t.log.Append("gateway both-hot episode start")
		} else {
			t.lastHot = now
		}
	} else if t.inEpisode {
		if now.Sub(t.lastHot) >= 3*time.Minute {
			ended = true
			t.inEpisode = false
			t.log.Append("gateway both-hot episode end")
		}
	}

	budget := t.normal
	if t.inEpisode && !ended {
		budget = t.lowered
	}

	return budget, ended
}

// Admit returns true if a new loop can be admitted given the number of running loops.
func (t *Tracker) Admit(running int) bool {
	budget := t.normal
	if t.inEpisode {
		budget = t.lowered
	}
	return running < budget
}
