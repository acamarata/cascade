package sessions_test

import (
	"testing"

	"github.com/acamarata/cascade/internal/fleet/sessions"
)

// TestWeighZeroSignalsSentinel proves the zero-signal case is exactly
// ConfidenceNone, including a nil slice and an all-absent slice.
func TestWeighZeroSignalsSentinel(t *testing.T) {
	scorer := sessions.NewConfidenceScorer()
	if got := scorer.Weigh(nil); got != sessions.ConfidenceNone {
		t.Fatalf("Weigh(nil) = %v, want ConfidenceNone", got)
	}
	allAbsent := []sessions.Signal{
		{Kind: sessions.SignalProcessPresent, Present: false},
		{Kind: sessions.SignalRecentPrompt, Present: false},
	}
	if got := scorer.Weigh(allAbsent); got != sessions.ConfidenceNone {
		t.Fatalf("Weigh(all absent) = %v, want ConfidenceNone", got)
	}
}

// TestWeighSaturatedSignalsSentinel proves every registered signal
// present yields exactly ConfidenceFull (also covered via config_test.go
// as a config-table structural check; this test asserts the same thing
// as confidence.go's own contract, independent of config.go's contents).
func TestWeighSaturatedSignalsSentinel(t *testing.T) {
	scorer := sessions.NewConfidenceScorer()
	full := []sessions.Signal{
		{Kind: sessions.SignalProcessPresent, Present: true},
		{Kind: sessions.SignalRecentPrompt, Present: true},
		{Kind: sessions.SignalRecentTool, Present: true},
		{Kind: sessions.SignalToolActivity, Present: true},
		{Kind: sessions.SignalTranscriptParseable, Present: true},
		{Kind: sessions.SignalStreamOpen, Present: true},
	}
	if got := scorer.Weigh(full); got != sessions.ConfidenceFull {
		t.Fatalf("Weigh(all present) = %v, want ConfidenceFull", got)
	}
}

// TestWeighUnrecognizedKindContributesZero proves SignalUnknown (and any
// kind absent from the registered table) is fail-closed: it never
// silently upgrades the score.
func TestWeighUnrecognizedKindContributesZero(t *testing.T) {
	scorer := sessions.NewConfidenceScorer()
	got := scorer.Weigh([]sessions.Signal{
		{Kind: sessions.SignalUnknown, Present: true},
		{Kind: sessions.SignalKind(99), Present: true},
	})
	if got != sessions.ConfidenceNone {
		t.Fatalf("Weigh(unrecognized kinds) = %v, want ConfidenceNone", got)
	}
}

// TestWeighDuplicateKindNotDoubleCounted proves a repeated Signal.Kind
// cannot inflate the score past that kind's single registered weight.
func TestWeighDuplicateKindNotDoubleCounted(t *testing.T) {
	scorer := sessions.NewConfidenceScorer()
	once := scorer.Weigh([]sessions.Signal{
		{Kind: sessions.SignalProcessPresent, Present: true},
	})
	dup := scorer.Weigh([]sessions.Signal{
		{Kind: sessions.SignalProcessPresent, Present: true},
		{Kind: sessions.SignalProcessPresent, Present: true},
		{Kind: sessions.SignalProcessPresent, Present: true},
	})
	if dup != once {
		t.Fatalf("Weigh(duplicated signal) = %v, want %v (same as counted once)", dup, once)
	}
}

// TestWeighDeterministic proves identical input slices yield identical
// scores across repeated calls, and that Weigh never mutates its input.
func TestWeighDeterministic(t *testing.T) {
	scorer := sessions.NewConfidenceScorer()
	signals := []sessions.Signal{
		{Kind: sessions.SignalRecentPrompt, Present: true},
		{Kind: sessions.SignalRecentTool, Present: false},
	}
	first := scorer.Weigh(signals)
	for i := 0; i < 5; i++ {
		if got := scorer.Weigh(signals); got != first {
			t.Fatalf("Weigh call %d = %v, want %v (non-deterministic)", i, got, first)
		}
	}
	if !signals[0].Present || signals[1].Present {
		t.Fatalf("Weigh mutated its input slice: %+v", signals)
	}
}

// TestWeighNeverExitsRange fuzzes the six registered SignalKinds across
// every present/absent combination and proves the result always stays
// within [ConfidenceNone, ConfidenceFull].
func TestWeighNeverExitsRange(t *testing.T) {
	scorer := sessions.NewConfidenceScorer()
	kinds := []sessions.SignalKind{
		sessions.SignalProcessPresent,
		sessions.SignalRecentPrompt,
		sessions.SignalRecentTool,
		sessions.SignalToolActivity,
		sessions.SignalTranscriptParseable,
		sessions.SignalStreamOpen,
	}
	for mask := 0; mask < 1<<len(kinds); mask++ {
		var signals []sessions.Signal
		for i, k := range kinds {
			signals = append(signals, sessions.Signal{Kind: k, Present: mask&(1<<i) != 0})
		}
		got := scorer.Weigh(signals)
		if got < sessions.ConfidenceNone || got > sessions.ConfidenceFull {
			t.Fatalf("Weigh(mask=%d) = %v, out of [%v, %v]", mask, got, sessions.ConfidenceNone, sessions.ConfidenceFull)
		}
	}
}
