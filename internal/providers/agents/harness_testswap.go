// Purpose: a test-only seam to stand a fake driver in for a registered harness.
// Every harness in the closed set is registered by production code at init, so
// a test can no longer register its own row; it swaps one in and restores it.
// Constraints: refuses outside a test binary; validates like RegisterHarness.

package agents

import (
	"fmt"
	"testing"
)

// SwapHarnessForTest replaces the binding and driver factory of an already
// registered harness and returns a function that puts the previous row back.
// It panics outside a test binary, on an unregistered harness, a malformed
// row or a nil factory.
func SwapHarnessForTest(b Binding, f DriverFactory) (restore func()) {
	if !testing.Testing() {
		panic("agents: SwapHarnessForTest called outside a test binary")
	}
	if err := validateBinding(b); err != nil {
		panic(fmt.Sprintf("agents: SwapHarnessForTest(%q): %v", b.Harness, err))
	}
	if f == nil {
		panic(fmt.Sprintf("agents: SwapHarnessForTest(%q): nil driver factory", b.Harness))
	}
	global.mu.Lock()
	defer global.mu.Unlock()
	prev, ok := global.rows[b.Harness]
	if !ok {
		panic(fmt.Sprintf("agents: SwapHarnessForTest(%q): harness is not registered", b.Harness))
	}
	global.rows[b.Harness] = entry{binding: copyBinding(b), factory: f}
	return func() {
		global.mu.Lock()
		defer global.mu.Unlock()
		global.rows[b.Harness] = prev
	}
}
