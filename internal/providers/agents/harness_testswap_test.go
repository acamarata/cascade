package agents

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func factoryOf(t *testing.T, id HarnessID) DriverFactory {
	t.Helper()
	global.mu.RLock()
	defer global.mu.RUnlock()
	e, ok := global.rows[id]
	if !ok {
		t.Fatalf("harness %q not registered", id)
	}
	return e.factory
}

func TestSwapHarnessForTestReplacesAndRestores(t *testing.T) {
	orig := global.rows[HarnessOpenCode]
	calls := 0
	swapped := func(SpawnSite) (provider.AgentProvider, error) { calls++; return nil, nil }
	restore := SwapHarnessForTest(orig.binding, swapped)
	if _, err := factoryOf(t, HarnessOpenCode)(SpawnSite{}); err != nil || calls != 1 {
		t.Fatalf("swapped factory: calls %d, err %v; want the swapped factory to run once", calls, err)
	}
	restore()
	global.mu.RLock()
	back := global.rows[HarnessOpenCode]
	global.mu.RUnlock()
	if back.binding.Harness != orig.binding.Harness || back.binding.LaneID != orig.binding.LaneID {
		t.Fatalf("restore left binding %+v, want %+v", back.binding, orig.binding)
	}
	_, _ = factoryOf(t, HarnessOpenCode)(SpawnSite{})
	if calls != 1 {
		t.Fatalf("after restore the swapped factory still ran (calls %d)", calls)
	}
}

func TestSwapHarnessForTestRefusesUnregisteredAndNil(t *testing.T) {
	for name, fn := range map[string]func(){
		"nil factory": func() { SwapHarnessForTest(global.rows[HarnessOpenCode].binding, nil) },
		"unknown harness": func() {
			b := global.rows[HarnessOpenCode].binding
			b.Harness = "not-a-harness"
			SwapHarnessForTest(b, func(SpawnSite) (provider.AgentProvider, error) { return nil, nil })
		},
	} {
		msg := func() (msg string) {
			defer func() { msg, _ = recover().(string) }()
			fn()
			return ""
		}()
		if !strings.Contains(msg, "SwapHarnessForTest") {
			t.Errorf("%s: panic %q, want a SwapHarnessForTest refusal", name, msg)
		}
	}
}
