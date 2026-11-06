package executive

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
)

func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
}

func pct(v float64) *float64 { return &v }

func ac7Source() map[economics.HealthKey]economics.KeyHealth {
	return map[economics.HealthKey]economics.KeyHealth{
		economics.HealthKeyExecutive: {State: economics.HealthStateProtected, Percentage: pct(0.12)},
		economics.HealthKeyBuild:     {State: economics.HealthStateHealthy, Percentage: pct(0.9)},
		economics.HealthKeyBulk:      {State: economics.HealthStateScarce},
		economics.HealthKeyAdversary: {State: economics.HealthStateAbundant, Percentage: pct(0.75)},
	}
}

func TestEXE14LegacyAC7(t *testing.T) {
	isolateHome(t)
	src := ac7Source()
	got, err := BuildHealthSummary(src)
	if err != nil {
		t.Fatalf("BuildHealthSummary: %v", err)
	}
	want := []struct {
		key   economics.HealthKey
		state economics.HealthState
		pct   *float64
	}{
		{economics.HealthKeyExecutive, economics.HealthStateProtected, pct(0.12)},
		{economics.HealthKeyBuild, economics.HealthStateHealthy, pct(0.9)},
		{economics.HealthKeyBulk, economics.HealthStateScarce, nil},
		{economics.HealthKeyAdversary, economics.HealthStateAbundant, pct(0.75)},
		{economics.HealthKeyLongContext, economics.HealthStateUnavailable, nil},
	}
	for i, w := range want {
		k := got.Keys[i]
		if k.Key != w.key || k.State != w.state {
			t.Fatalf("key %d = %s/%s, want %s/%s", i, k.Key, k.State, w.key, w.state)
		}
		if (k.Percentage == nil) != (w.pct == nil) || (w.pct != nil && *k.Percentage != *w.pct) {
			t.Fatalf("key %s percentage = %v, want %v", k.Key, k.Percentage, w.pct)
		}
	}
	*src[economics.HealthKeyExecutive].Percentage = 0.99
	if *got.Keys[0].Percentage != 0.12 {
		t.Fatalf("summary aliased the source percentage: %v", *got.Keys[0].Percentage)
	}
}

func TestHealthSummaryRefusesUnknownKeyOrState(t *testing.T) {
	isolateHome(t)
	badKey := ac7Source()
	badKey["gpu"] = economics.KeyHealth{State: economics.HealthStateHealthy}
	badState := ac7Source()
	badState[economics.HealthKeyBuild] = economics.KeyHealth{State: "ok"}
	for name, src := range map[string]map[economics.HealthKey]economics.KeyHealth{"key": badKey, "state": badState} {
		got, err := BuildHealthSummary(src)
		if !errors.Is(err, ErrHealthSource) {
			t.Fatalf("%s: err = %v, want ErrHealthSource", name, err)
		}
		if got != (HealthSummary{}) {
			t.Fatalf("%s: summary = %+v, want zero", name, got)
		}
	}
	got, err := BuildHealthSummary(nil)
	if err != nil {
		t.Fatalf("nil map: %v", err)
	}
	for _, k := range got.Keys {
		if k.State != economics.HealthStateUnavailable || k.Percentage != nil || !k.Key.Valid() {
			t.Fatalf("nil map key = %+v, want unavailable", k)
		}
	}
}

func TestSummarizeFleetHealthCallsHealthOnce(t *testing.T) {
	isolateHome(t)
	calls := 0
	ok := func(context.Context) (map[economics.HealthKey]economics.KeyHealth, error) {
		calls++
		return ac7Source(), nil
	}
	got, err := SummarizeFleetHealth(context.Background(), ok)
	if err != nil || calls != 1 {
		t.Fatalf("err = %v, calls = %d, want nil and 1", err, calls)
	}
	if got.Keys[1].State != economics.HealthStateHealthy {
		t.Fatalf("build = %+v", got.Keys[1])
	}

	injected := errors.New("read failed")
	calls = 0
	fail := func(context.Context) (map[economics.HealthKey]economics.KeyHealth, error) {
		calls++
		return nil, injected
	}
	got, err = SummarizeFleetHealth(context.Background(), fail)
	if calls != 1 || got != (HealthSummary{}) || !errors.Is(err, injected) {
		t.Fatalf("calls = %d, summary = %+v, err = %v", calls, got, err)
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v, want KindUnavailable", kind)
	}
	for _, k := range got.Keys {
		if k.State == economics.HealthStateHealthy {
			t.Fatalf("error path reported a healthy key")
		}
	}

	_, err = SummarizeFleetHealth(context.Background(), nil)
	if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
		t.Fatalf("nil health: kind = %v, err = %v, want KindInvalidInput", kind, err)
	}
}

func TestHealthSummaryJSONShape(t *testing.T) {
	isolateHome(t)
	got, err := BuildHealthSummary(ac7Source())
	if err != nil {
		t.Fatalf("BuildHealthSummary: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	const want = `{"keys":[{"key":"executive","state":"protected","percentage":0.12},{"key":"build","state":"healthy","percentage":0.9},{"key":"bulk","state":"scarce"},{"key":"adversary","state":"abundant","percentage":0.75},{"key":"long_context","state":"unavailable"}]}`
	if string(b) != want {
		t.Fatalf("json =\n%s\nwant\n%s", b, want)
	}
}
