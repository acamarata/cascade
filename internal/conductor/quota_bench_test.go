package conductor

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

// BenchmarkQuotaNextLane measures NextLane's latency under sustained
// 429-churn: every call demotes the current lane and asks for the next
// one, cycling through a five-lane spill order repeatedly. This is the
// A-T3 bench-lane budget baseline 06-FORGE-SPEC §5.13 requires; AB/S-58.T2
// asserts the numeric budget against this benchmark's recorded result.
func BenchmarkQuotaNextLane(b *testing.B) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"a", "b", "c", "d", "e"}}, clock)
	pub := &fakeEventPublisher{}
	ctx := context.Background()
	lanes := []LaneID{"a", "b", "c", "d", "e"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		current := lanes[i%len(lanes)]
		if _, err := p.Advance(ctx, pub, "bench", current, nil, "429"); err != nil {
			// Exhaustion is an expected steady-state outcome once every
			// lane in the window is demoted; the clock advance below
			// clears it before the next iteration needs a fresh lane.
			clock.Advance(defaultRateLimitWindow + time.Second)
		}
	}
}
