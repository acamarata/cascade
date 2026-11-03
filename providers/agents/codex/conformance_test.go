package codex

import (
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/conformance"
)

// conformanceCaseCount is the conformance inventory at the base.
const conformanceCaseCount = 20

// TestCodexConformanceReplay runs every Suite case against the replayed
// session through the replay App Server with Suite{Env, Worktree, Filter}.
func TestCodexConformanceReplay(t *testing.T) {
	k := newKit(t)
	k.opts.Log = false // concurrent jobs share the profile dir
	_ = k.driver()     // writes replay.json
	var made []*Driver
	t.Cleanup(func() {
		for _, d := range made {
			cancelAll(d)
		}
	})
	s := &conformance.Suite{
		Worktree: k.root, Env: k.env(), Filter: identity,
		New: func() (provider.AgentProvider, error) {
			d, err := New(k.cfg)
			if err == nil {
				made = append(made, d)
			}
			return d, err
		},
	}
	v := reflect.ValueOf(s)
	typ := v.Type()
	ran := 0
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		if !strings.HasPrefix(m.Name, "Test") {
			continue
		}
		fn, ok := v.Method(i).Interface().(func(*testing.T))
		if !ok {
			continue
		}
		ran++
		t.Run(m.Name, fn)
	}
	if ran != conformanceCaseCount {
		t.Fatalf("ran %d conformance cases, want %d", ran, conformanceCaseCount)
	}
}
