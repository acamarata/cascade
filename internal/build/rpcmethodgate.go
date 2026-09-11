// Package build (this file) implements R-16.80 Ruling 3(c)'s derived
// gate: every cmd/cascade `<x>.Do(ctx, <method>, params, out)` JSON-RPC
// call site names a method the daemon is supposed to answer; this file
// proves each one has a matching `<y>.Register(<method>, ...)` call
// somewhere in the tracked tree, mechanically, with no hand-maintained
// method list on either side
// (the same shape ledgeridentitygate.go and internal/build/
// testonlygate.go already use, and the same reasoning R-16.80 itself
// gives for why a hand-maintained list "would inherit the very bug it
// checks for").
//
// WHAT THIS GATE CANNOT CATCH (stated per this package's convention):
//   - A `.Do` call whose method argument is not a bare string literal, a
//     same-file const, or a qualified <pkg>.Const this gate can resolve
//     by reading that package's own const declarations, is invisible to
//     the coverage check. cmd/cascade/memory.go and cmd/cascade/recall.go
//     both dispatch through a `method string` PARAMETER threaded in by
//     their own callers rather than a literal at the .Do call site
//     itself - this gate reports those as RPCMethodUnresolved rather than
//     silently treating them as covered, exactly as R-16.80 requires
//     ("if it proves underivable... state precisely why").
//   - The four-argument `Do(ctx, method, params, out)` shape is what
//     distinguishes a JSON-RPC caller from the unrelated two-argument
//     `Do(ctx, req)` HTTP-Doer interface (providers/*/stream.go,
//     internal/ci/poll.go, internal/providers/intake/transport.go): a
//     future JSON-RPC caller wrapper that drops or adds an argument
//     would silently stop matching this shape.
//   - Registration only counts a literal-or-resolvable-const first
//     argument to a call named "Register" (e.g. `registry.Register(...)`,
//     `x.Register(...)`); a registration helper that builds the method
//     name at runtime (string concatenation with a non-const,
//     fmt.Sprintf, etc.) would also be invisible - none exists in this
//     tree today.
//
// SPORT: internal.build.CheckRPCMethodCoverage/ADDED (R-16.80).
package build

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RPCMethodGateSkipsPath reports whether rel is this gate's own seeded-
// violation fixture data, which the REAL-TREE scan must not read -
// identical rationale to LedgerIdentitySkipsPath: the fixture under
// testdata/seeded-violations/rpc-method-gate/ deliberately contains an
// unregistered method so the gate has something to catch, and scanning
// it against the live tree would make TestRPCMethodGate_RealTreeGreen
// permanently red on the gate's own test data.
func RPCMethodGateSkipsPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "testdata" {
			return true
		}
	}
	return false
}

// CheckRPCMethodCoverageTracked is the REAL-TREE entry point: it drops
// fixture paths per RPCMethodGateSkipsPath, then defers to
// CheckRPCMethodCoverage. The seeded-violation test passes its fixture
// path to CheckRPCMethodCoverage directly, or the filter would silently
// swallow the very violation it exists to prove the gate catches.
func CheckRPCMethodCoverageTracked(root string, files []string) ([]RPCMethodViolation, []RPCMethodUnresolved, error) {
	kept := make([]string, 0, len(files))
	for _, rel := range files {
		if RPCMethodGateSkipsPath(rel) {
			continue
		}
		kept = append(kept, rel)
	}
	return CheckRPCMethodCoverage(root, kept)
}

// RPCMethodViolation is one cmd/cascade `c.Do(ctx, <method>, ...)` call
// site whose resolved method name has no matching Register(...) call
// anywhere in the tracked tree - the exact defect class R-16.80 found
// ("conductor.execute" had a caller and no handler).
type RPCMethodViolation struct {
	File   string
	Line   int
	Method string
}

// String renders one violation for gate failure output.
func (v RPCMethodViolation) String() string {
	return fmt.Sprintf("%s:%d: c.Do dials %q, which nothing in the tracked tree registers (R-16.80 Ruling 3)",
		v.File, v.Line, v.Method)
}

// RPCMethodUnresolved names one cmd/cascade `.Do` call site this gate
// could not resolve to a literal method name - see this file's package
// doc comment for the two known cases (memory.go, recall.go) and why a
// dynamic method name is genuinely outside this gate's derivation.
type RPCMethodUnresolved struct {
	File string
	Line int
}

// String renders one unresolved call site for gate output.
func (v RPCMethodUnresolved) String() string {
	return fmt.Sprintf("%s:%d: c.Do's method argument is not a string literal or a resolvable const (dynamic method name, outside this gate's derivation)",
		v.File, v.Line)
}

// CheckRPCMethodCoverage scans every non-test .go file in files (module-
// relative paths, as ListTrackedFiles returns) for `<x>.Do(ctx, <method>,
// params, out)` call sites, resolves each <method> argument, and reports
// every resolved method with no matching `<y>.Register(<method>, ...)`
// call anywhere in files. A method this gate cannot resolve to a literal
// is reported in the second return value instead of being silently
// counted as covered.
func CheckRPCMethodCoverage(root string, files []string) ([]RPCMethodViolation, []RPCMethodUnresolved, error) {
	registered, err := collectRegisteredMethods(root, files)
	if err != nil {
		return nil, nil, err
	}

	var violations []RPCMethodViolation
	var unresolved []RPCMethodUnresolved
	cache := map[string]constLiteralMap{}

	for _, rel := range sortedCopy(files) {
		if !isCLISourceFile(rel) {
			continue
		}
		calls, err := findDoCalls(root, rel)
		if err != nil {
			return nil, nil, err
		}
		for _, call := range calls {
			method, ok := resolveMethodArg(root, rel, call.methodArg, cache)
			if !ok {
				unresolved = append(unresolved, RPCMethodUnresolved{File: rel, Line: call.line})
				continue
			}
			if !registered[method] {
				violations = append(violations, RPCMethodViolation{File: rel, Line: call.line, Method: method})
			}
		}
	}
	return violations, unresolved, nil
}
