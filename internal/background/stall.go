package background

// StallPrompt is the exact escalation text shown when the autopilot has
// produced two consecutive turns with no file changes.
const StallPrompt = "D4724: Two consecutive autopilot turns changed no files. Give one concrete way out, or reply stop."

// StallTracker tracks consecutive autopilot turns that changed no files.
// Its zero value is ready to use.
type StallTracker struct {
	// Unchanged counts consecutive observations with no file changes,
	// saturating at two.
	Unchanged int

	// Pending reports whether the stall prompt is due.
	Pending bool
}

// Observe records whether the latest autopilot turn changed any files.
// It returns the updated consecutive-unchanged count.
//
// Observe(true) resets the tracker to zero and clears any pending prompt.
// Observe(false) increments the count until two, saturates at two, and marks
// the prompt pending when the count reaches two. Negative counts normalize to
// zero before observation, and counts above two normalize to two.
//
// A nil receiver is safe and returns 0 without side effects.
func (s *StallTracker) Observe(changed bool) int {
	if s == nil {
		return 0
	}

	if s.Unchanged < 0 {
		s.Unchanged = 0
	}
	if s.Unchanged > 2 {
		s.Unchanged = 2
	}

	if changed {
		s.Unchanged = 0
		s.Pending = false
		return 0
	}

	if s.Unchanged < 2 {
		s.Unchanged++
	}
	if s.Unchanged == 2 {
		s.Pending = true
	}

	return s.Unchanged
}

// Prompt returns StallPrompt exactly when a stall prompt is pending, and
// otherwise returns an empty string. It does not mutate the tracker.
//
// A nil receiver returns an empty string.
func (s *StallTracker) Prompt() string {
	if s == nil {
		return ""
	}
	if s.Pending {
		return StallPrompt
	}
	return ""
}
