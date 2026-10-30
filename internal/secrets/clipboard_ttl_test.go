// Purpose: the clipboard_ttl range gate and the writer constructor's
//
//	required seams.
//
// SPORT: internal/secrets ParseClipboardTTL/TEST (P1-SEC-09).
package secrets

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseClipboardTTLDefaultAndRange(t *testing.T) {
	ok := map[string]struct {
		in   map[string]interface{}
		want time.Duration
	}{
		"nil section":   {nil, 30 * time.Second},
		"absent":        {map[string]interface{}{}, 30 * time.Second},
		"lower bound":   {map[string]interface{}{"clipboard_ttl": "5s"}, 5 * time.Second},
		"upper bound":   {map[string]interface{}{"clipboard_ttl": 10 * time.Minute}, 10 * time.Minute},
		"whole seconds": {map[string]interface{}{"clipboard_ttl": int64(45)}, 45 * time.Second},
		"int seconds":   {map[string]interface{}{"clipboard_ttl": 600}, 10 * time.Minute},
	}
	for name, tc := range ok {
		if got, err := ParseClipboardTTL(tc.in); err != nil || got != tc.want {
			t.Fatalf("%s: ParseClipboardTTL = %s, %v; want %s", name, got, err, tc.want)
		}
	}
	for name, v := range map[string]interface{}{
		"1s": "1s", "4.999s": "4999ms", "over 10m": "10m1s", "negative": int64(-5), "huge": int64(1 << 62),
		"601 seconds": 601, "zero": time.Duration(0), "not a duration": "soon", "bool": true,
	} {
		_, err := ParseClipboardTTL(map[string]interface{}{"clipboard_ttl": v})
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(fmt.Sprint(err), "secrets.clipboard_ttl") {
			t.Fatalf("%s: ParseClipboardTTL = %v, want KindInvalidInput naming secrets.clipboard_ttl", name, err)
		}
	}
}

// effectsOnly is an EffectWriter with no unknown-outcome mark.
type effectsOnly struct{ audit.EffectWriter }

func TestNewClipboardWriterRequiresAllSeams(t *testing.T) {
	r := newRig()
	clock := runtime.NewFixedClock(clipNow)
	v := fakeVerifier{verb: ClipboardApprovalVerb}
	cases := map[string]func() (ClipboardWriter, error){
		"nil clock": func() (ClipboardWriter, error) {
			return newClipboardWriterWithOps(nil, r.aw, r.fx, v, r.store, r.sched, r.ops, time.Minute)
		},
		"nil audit": func() (ClipboardWriter, error) {
			return newClipboardWriterWithOps(clock, nil, r.fx, v, r.store, r.sched, r.ops, time.Minute)
		},
		"nil effects": func() (ClipboardWriter, error) {
			return newClipboardWriterWithOps(clock, r.aw, nil, v, r.store, r.sched, r.ops, time.Minute)
		},
		"no unknown": func() (ClipboardWriter, error) {
			return newClipboardWriterWithOps(clock, r.aw, effectsOnly{r.fx}, v, r.store, r.sched, r.ops, time.Minute)
		},
		"nil verifier": func() (ClipboardWriter, error) {
			return newClipboardWriterWithOps(clock, r.aw, r.fx, nil, r.store, r.sched, r.ops, time.Minute)
		},
		"nil store": func() (ClipboardWriter, error) {
			return newClipboardWriterWithOps(clock, r.aw, r.fx, v, nil, r.sched, r.ops, time.Minute)
		},
		"nil scheduler": func() (ClipboardWriter, error) {
			return newClipboardWriterWithOps(clock, r.aw, r.fx, v, r.store, nil, r.ops, time.Minute)
		},
		"nil ops": func() (ClipboardWriter, error) {
			return newClipboardWriterWithOps(clock, r.aw, r.fx, v, r.store, r.sched, nil, time.Minute)
		},
		"ttl 1s": func() (ClipboardWriter, error) {
			return newClipboardWriterWithOps(clock, r.aw, r.fx, v, r.store, r.sched, r.ops, time.Second)
		},
	}
	for name, build := range cases {
		if w, err := build(); w != nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: = (%v, %v), want a KindInvalidInput refusal", name, w, err)
		}
	}
	if _, err := NewClipboardWriter(clock, r.aw, r.fx, v, r.store, r.sched, 30*time.Second); err != nil {
		t.Fatalf("NewClipboardWriter over the host backend: %v", err)
	}
	if err := RearmHostPendingClears(t.Context(), clock, effectsOnly{r.fx}, r.store, r.sched); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("RearmHostPendingClears without recovery = %v, want KindInvalidInput", err)
	}
}

// TestClipboardUnsupportedRefuses: a host without the tool refuses with
// KindUnsupported before any record or row.
func TestClipboardUnsupportedRefuses(t *testing.T) {
	r := newRig()
	r.ops.probeErr = cascade.Wrap(cascade.KindUnsupported, ErrClipboardUnsupported, "exec: not found")
	err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v"))
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("Write = %v, want KindUnsupported", err)
	}
	if got := r.tr.list(); !slices.Equal(got, []string{"probe"}) || r.store.len() != 0 {
		t.Fatalf("trace = %v, rows = %d; want only the probe", got, r.store.len())
	}
}
