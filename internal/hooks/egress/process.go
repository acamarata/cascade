// Purpose: the process egress firewall seam. One composition root (the
//
//	cascade binary) installs ONE factory that binds a registry to the
//	process's real vault and detector; every internal package asks
//	ProcessEngine for its engine instead of building one, so no package
//	can quietly bind an empty or fake value source.
//
// Inputs: the factory the composition root installs, and the registry a
//
//	caller enforces.
//
// Outputs: a ready *Engine, or a typed refusal. With no factory installed
//
//	the answer is ErrNoProcessFirewall, never an engine: a firewall that
//	nobody configured must refuse, not pass content through or run only
//	the shape-based half of the substitution pass.
//
// Constraints: resolved per call, never at package init, so a test binary
//
//	or a process that never installs a firewall fails closed at the call
//	site that needed one. A factory error is returned unchanged so a
//	custody failure keeps its Kind and cause.
//
// SPORT: EGRESS_PROCESS_FIREWALL: ADD (internal/hooks/egress ProcessEngine).

package egress

import (
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrNoProcessFirewall is the refusal ProcessEngine returns when no
// composition root installed a factory.
var ErrNoProcessFirewall = cascade.New(cascade.KindUnavailable, "egress: no process firewall is installed")

// processEngineFactory holds the installed factory.
var processEngineFactory struct {
	mu sync.RWMutex
	f  func(*Registry) (*Engine, error)
}

// SetProcessEngineFactory installs the factory ProcessEngine calls. nil
// detaches it, after which ProcessEngine refuses again.
func SetProcessEngineFactory(f func(*Registry) (*Engine, error)) {
	processEngineFactory.mu.Lock()
	defer processEngineFactory.mu.Unlock()
	processEngineFactory.f = f
}

// ProcessEngine returns the process firewall's engine over registry.
//
// No factory installed is ErrNoProcessFirewall. A nil registry is refused
// before the factory runs. A factory error is returned unchanged. A
// factory that answers with neither an engine nor an error is a defect,
// refused here rather than handed to a caller that would only learn of it
// as "no engine configured" on its first write.
func ProcessEngine(registry *Registry) (*Engine, error) {
	processEngineFactory.mu.RLock()
	f := processEngineFactory.f
	processEngineFactory.mu.RUnlock()
	if f == nil {
		return nil, ErrNoProcessFirewall
	}
	if registry == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "egress: the process firewall needs a registry")
	}
	engine, err := f(registry)
	if err != nil {
		return nil, err
	}
	if engine == nil {
		return nil, cascade.New(cascade.KindInternal,
			"egress: the process firewall factory returned no engine and no error")
	}
	return engine, nil
}
