// Purpose: the hysteresis filter (component 3 of the four-component
//   segmenter): decides whether a CANDIDATE boundary is a real topic change
//   or flicker, by looking at what happens just after it.
// Inputs: the per-transition distance-spike map segmenter_core.go computed
//   for the whole window (spikes[i] reports whether the distance between
//   turns i-1 and i cleared HysteresisConfig.Threshold), plus the index of
//   the candidate under test.
// Outputs: whether that candidate commits a Boundary.
// Constraints: unexported - hysteresisFilter is Segment's own internal
//   decision rule, constructed and driven only from segmenter_core.go
//   within this package (no other ticket's contract names it as a
//   dependency; S-45.T3's AutoThreader consumes Segmenter/Boundary only).
//   Deterministic: no clock, no randomness, no I/O, no internal state
//   carried between calls.
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).

package topics

// hysteresisFilter is the hysteresis decision rule. It holds no running
// counter: the shape a real topic change has in the distance signal is ONE
// spike between two adjacent turns followed by low internal distances
// inside the new topic, so the evidence a candidate needs lies AFTER it,
// not before it. Build one with newHysteresisFilter.
//
// WHY NOT "N CONSECUTIVE SPIKES". Counting N consecutive spiking
// transitions before committing measures the opposite of a topic change:
// consecutive spikes mean consecutive turns that resemble neither their
// predecessor nor each other, which is churn, and a topic that actually
// holds produces exactly one spike and then stops spiking. A counter also
// reports the boundary at the turn where the count finally reached N,
// which is Window-1 turns after the turn that actually started the new
// topic, so every downstream consumer (and every corpus comparison)
// receives a systematically late index.
type hysteresisFilter struct {
	cfg HysteresisConfig
}

// newHysteresisFilter validates cfg and returns a ready hysteresisFilter.
// A caller with an already-validated HysteresisConfig (NewSegmenter calls
// HysteresisConfig.Validate itself before this) never observes the error
// return in practice; this function validates again anyway because a
// hysteresisFilter built with Window<1 would have no confirmation rule at
// all, which is a correctness bug rather than a documentation gap if this
// constructor is ever called from a second, less careful site.
func newHysteresisFilter(cfg HysteresisConfig) (*hysteresisFilter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &hysteresisFilter{cfg: cfg}, nil
}

// confirm reports whether the candidate boundary at transition index
// candidate commits. candidate is the index of the FIRST turn of the
// proposed new segment: spikes[candidate] is true and the classifier's
// label there differs from the last committed topic, both already
// established by segmenter_core.go before it calls this.
//
// The confirmation window is the next Window-1 transitions,
// candidate+1 .. candidate+Window-1. The candidate commits when none of
// them spikes: the new topic held its ground. A spike inside that window
// is flicker - a spike immediately answered by a counter-spike, the
// one-turn excursion hysteresis exists to suppress - and rejects the
// candidate. Window=1 has no confirmation window at all and commits on
// the spike alone, which is the documented "hysteresis disabled" setting.
//
// TAIL CASE (window not observable). When the confirmation window extends
// past the last transition - a topic change on the final turn, or within
// Window-1 turns of it - the evidence the rule wants cannot exist yet.
// The candidate commits PROVISIONALLY on the evidence available, rather
// than being withheld: withholding would make a genuine final-turn topic
// change systematically unreportable, and this ticket has no surface on
// which to carry a deferred candidate into a later Segment call (Segment
// is a pure function of the window it is handed). A caller that needs the
// distinction re-segments a window that extends past the boundary. This
// choice is stated on Boundary itself (segmenter_types.go) because it is
// visible in Segment's output, not merely internal.
func (f *hysteresisFilter) confirm(spikes []bool, candidate int) bool {
	end := candidate + f.cfg.Window // first transition past the confirmation window
	for j := candidate + 1; j < end && j < len(spikes); j++ {
		if spikes[j] {
			return false
		}
	}
	return true
}
