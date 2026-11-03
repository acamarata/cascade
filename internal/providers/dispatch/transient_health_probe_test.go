// Purpose: the probe-context skip marker on the transient counter
//
//	(P1-TOP-19): calls under WithoutTransientCount neither add to nor
//	reset the P1-GWY-09 consecutive-transient count.
//
// Inputs: constructed transient errors and a recording health writer.
//
// Outputs: none.
// Constraints: no network; no clock; credentials are absent.
//
// SPORT: internal/providers/dispatch transient_health/ADD (P1-TOP-19).

package dispatch

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// countOf reads the counter's current value for name.
func countOf(c *transientCounter, name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[name]
}

func TestTransientCounterSkipsProbeContext(t *testing.T) {
	ctx := context.Background()
	h, c := &recordingHealth{}, &transientCounter{}
	o := observer(h, c, "P")
	o.observe(ctx, transientErr())
	o.observe(ctx, transientErr())
	if got := countOf(c, "P"); got != 2 {
		t.Fatalf("count after two transients = %d, want 2", got)
	}

	probe := WithoutTransientCount(ctx)
	for i := 0; i < 3; i++ {
		o.observe(probe, transientErr())
	}
	if n := h.count(); n != 0 {
		t.Fatalf("DemoteProvider calls under the marker = %d, want 0", n)
	}
	if got := countOf(c, "P"); got != 2 {
		t.Fatalf("count after marked transients = %d, want 2", got)
	}
	o.observe(probe, nil)
	if got := countOf(c, "P"); got != 2 {
		t.Fatalf("count after a marked success = %d, want 2 (not reset)", got)
	}

	o.observe(ctx, transientErr())
	h.mu.Lock()
	calls := append([]string(nil), h.calls...)
	h.mu.Unlock()
	want := "P/" + string(provider.ReasonTransientUpstream)
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("DemoteProvider calls = %v, want exactly [%s]", calls, want)
	}
	if got := countOf(c, "P"); got != 0 {
		t.Fatalf("count after the demotion = %d, want 0", got)
	}

	// Mutation check: the same sequence without the marker demotes on the
	// first 500, so the marker is what kept the count above.
	h2, c2 := &recordingHealth{}, &transientCounter{}
	o2 := observer(h2, c2, "P")
	o2.observe(ctx, transientErr())
	o2.observe(ctx, transientErr())
	o2.observe(ctx, transientErr())
	if n := h2.count(); n != 1 {
		t.Fatalf("unmarked third transient: DemoteProvider calls = %d, want 1", n)
	}
	if transientCountSkipped(ctx) || !transientCountSkipped(probe) {
		t.Fatal("transientCountSkipped must read only the marker")
	}
}
