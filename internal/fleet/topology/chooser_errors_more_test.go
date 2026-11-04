package topology

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestExtractRetryAfter(t *testing.T) {
	cases := []struct {
		msg  string
		want string
		ok   bool
	}{
		{"rate limited", "", false},
		{"slow down retry-after=30 please", "30", true},
		{"slow down retry-after=45", "45", true},
		{"retry-after= nothing", "", false},
	}
	for _, c := range cases {
		got, ok := extractRetryAfter(c.msg)
		if got != c.want || ok != c.ok {
			t.Errorf("extractRetryAfter(%q) = (%q, %v), want (%q, %v)", c.msg, got, ok, c.want, c.ok)
		}
	}
	if got := errMessage(nil); got != "" {
		t.Errorf("errMessage(nil) = %q, want empty", got)
	}
	if got := errMessage(errors.New("boom")); got != "boom" {
		t.Errorf("errMessage = %q, want boom", got)
	}
}

func TestOnProviderErrorRefusesUnclassifiableErrors(t *testing.T) {
	c := NewChooser(newTestClock(), 1, alwaysAllow, admitAllCreds, nil)
	ctx := context.Background()
	_, err := c.OnProviderError(ctx, "d1", "cred", errors.New("plain error"))
	if !errors.Is(err, ErrTopologyInvariant) {
		t.Fatalf("untyped error = %v, want ErrTopologyInvariant", err)
	}
	_, err = c.OnProviderError(ctx, "d1", "cred", cascade.New(cascade.KindInvalidInput, "bad"))
	if !errors.Is(err, ErrTopologyInvariant) {
		t.Fatalf("kind without transition = %v, want ErrTopologyInvariant", err)
	}
	_, err = c.OnProviderError(ctx, "d1", "", cascade.New(cascade.KindPermissionDenied, "401"))
	if !errors.Is(err, ErrTopologyInvariant) {
		t.Fatalf("401 without credential = %v, want ErrTopologyInvariant", err)
	}
	_, err = c.OnProviderError(ctx, "ghost", "cred", cascade.New(cascade.KindQuotaExhausted, "429"))
	if !errors.Is(err, ErrTopologyNotFound) {
		t.Fatalf("429 on unknown domain = %v, want ErrTopologyNotFound", err)
	}
}

func TestOn429HonoursRetryAfterAndBucketResetAt(t *testing.T) {
	clock := newTestClock()
	reset := clock.Now().Add(7 * time.Minute)
	dom := QuotaDomain{ID: "d1", AccountRef: "a1", Kind: QuotaDomainAPIProject,
		Dimensions: map[string]Bucket{DimensionRPM: {Name: DimensionRPM, Limit: 10, ResetAt: reset}}}
	c := NewChooser(clock, 1, alwaysAllow, admitAllCreds, nil)
	c.SetTopology([]Account{{ID: "a1"}}, []QuotaDomain{dom}, nil, nil)
	ctx := context.Background()

	r, err := c.OnProviderError(ctx, "d1", "", cascade.New(cascade.KindQuotaExhausted, "x retry-after=90 y"))
	if err != nil || r.After != 90*time.Second || !r.Reroute {
		t.Fatalf("retry-after 429 = %+v, %v; want After 90s with reroute", r, err)
	}
	r, err = c.OnProviderError(ctx, "d1", "", cascade.New(cascade.KindQuotaExhausted, "429 no hint"))
	if err != nil || r.After != 7*time.Minute {
		t.Fatalf("reset_at 429 = %+v, %v; want After 7m", r, err)
	}
}
