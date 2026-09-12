package cascadepa

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type stubDigestSubscriber struct {
	sig DigestSignal
	err error
}

func (s stubDigestSubscriber) CheckDigest(context.Context) (DigestSignal, error) {
	return s.sig, s.err
}

func withDigestSubscriber(t *testing.T, s DigestSubscriber) {
	t.Helper()
	SetDigestSubscriber(s)
	t.Cleanup(func() { SetDigestSubscriber(nil) })
}

// TestCheckDigestNudge_PendingGreaterThanZero proves a nonzero pending
// count produces a nudge naming the count and the command to run.
func TestCheckDigestNudge_PendingGreaterThanZero(t *testing.T) {
	withDigestSubscriber(t, stubDigestSubscriber{sig: DigestSignal{PendingCount: 3}})
	nudge, shown, err := CheckDigestNudge(context.Background())
	if err != nil || !shown {
		t.Fatalf("CheckDigestNudge: err=%v shown=%v", err, shown)
	}
	if !strings.Contains(nudge, "3") || !strings.Contains(nudge, "/memory review next") {
		t.Fatalf("nudge = %q, want it to name the count and the command", nudge)
	}
}

// TestCheckDigestNudge_PendingZeroSuppressed proves a zero pending count
// is suppressed, not nudged.
func TestCheckDigestNudge_PendingZeroSuppressed(t *testing.T) {
	withDigestSubscriber(t, stubDigestSubscriber{sig: DigestSignal{PendingCount: 0}})
	nudge, shown, err := CheckDigestNudge(context.Background())
	if err != nil || shown || nudge != "" {
		t.Fatalf("CheckDigestNudge = (%q, %v, %v), want suppressed", nudge, shown, err)
	}
}

// TestCheckDigestNudge_SubscriberError proves a subscriber failure is
// reported to the caller rather than silently treated as zero pending.
func TestCheckDigestNudge_SubscriberError(t *testing.T) {
	withDigestSubscriber(t, stubDigestSubscriber{err: cascade.New(cascade.KindUnavailable, "unreachable")})
	_, shown, err := CheckDigestNudge(context.Background())
	if shown {
		t.Fatal("CheckDigestNudge: want shown=false on a subscriber error")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// TestCheckDigestNudge_Unconfigured proves the default subscriber refuses
// rather than panicking or fabricating a signal.
func TestCheckDigestNudge_Unconfigured(t *testing.T) {
	SetDigestSubscriber(nil)
	_, shown, err := CheckDigestNudge(context.Background())
	if shown {
		t.Fatal("CheckDigestNudge: want shown=false when unconfigured")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// TestFormatDigestNudge_Singular proves the noun agrees at exactly one
// pending candidate (mutation-proof pairing for the plural case above).
func TestFormatDigestNudge_Singular(t *testing.T) {
	nudge, shown := FormatDigestNudge(DigestSignal{PendingCount: 1})
	if !shown {
		t.Fatal("FormatDigestNudge: want shown=true at PendingCount=1")
	}
	if !strings.Contains(nudge, "1 memory ") {
		t.Fatalf("nudge = %q, want singular %q", nudge, "1 memory ")
	}
}
