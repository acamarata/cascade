package cascade_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// wantExitCode and wantRPCCode pin the R-14.3 frozen tables verbatim, so a
// drift in codes.go's maps is caught by name, not just by uniqueness.
var wantExitCode = map[cascade.Kind]int{
	cascade.KindInternal:          1,
	cascade.KindInvalidInput:      2,
	cascade.KindNotFound:          3,
	cascade.KindConflict:          4,
	cascade.KindUnavailable:       5,
	cascade.KindTimeout:           6,
	cascade.KindPermissionDenied:  7,
	cascade.KindElevationRequired: 8,
	cascade.KindPolicyDenied:      9,
	cascade.KindCapabilityDenied:  10,
	cascade.KindQuotaExhausted:    11,
	cascade.KindUnsupported:       12,
	cascade.KindIntegrity:         13,
	cascade.KindCanceled:          130,
}

var wantRPCCode = map[cascade.Kind]int{
	cascade.KindInternal:          -32000,
	cascade.KindNotFound:          -32001,
	cascade.KindInvalidInput:      -32002,
	cascade.KindConflict:          -32003,
	cascade.KindUnavailable:       -32004,
	cascade.KindTimeout:           -32005,
	cascade.KindCanceled:          -32006,
	cascade.KindPermissionDenied:  -32007,
	cascade.KindElevationRequired: -32008,
	cascade.KindPolicyDenied:      -32009,
	cascade.KindCapabilityDenied:  -32010,
	cascade.KindQuotaExhausted:    -32011,
	cascade.KindUnsupported:       -32012,
	cascade.KindIntegrity:         -32013,
}

func TestExitCodeTable_MatchesR143(t *testing.T) {
	for _, k := range cascade.AllKinds() {
		want, ok := wantExitCode[k]
		if !ok {
			t.Fatalf("test table missing an expected exit code for kind %v", k)
		}
		if got := k.ExitCode(); got != want {
			t.Errorf("%v.ExitCode() = %d, want %d", k, got, want)
		}
	}

	var zero cascade.Kind
	if got := zero.ExitCode(); got != cascade.ExitInternal {
		t.Errorf("invalid Kind.ExitCode() = %d, want ExitInternal (%d) fallback", got, cascade.ExitInternal)
	}
}

func TestJSONRPCCodeTable_MatchesR143(t *testing.T) {
	for _, k := range cascade.AllKinds() {
		want, ok := wantRPCCode[k]
		if !ok {
			t.Fatalf("test table missing an expected JSON-RPC code for kind %v", k)
		}
		if got := k.JSONRPCCode(); got != want {
			t.Errorf("%v.JSONRPCCode() = %d, want %d", k, got, want)
		}
		// Every taxonomy code must sit outside the JSON-RPC 2.0
		// spec-reserved range (-32768..-32600).
		if want >= -32768 && want <= -32600 {
			t.Errorf("%v JSON-RPC code %d falls inside the spec-reserved range", k, want)
		}
	}

	var zero cascade.Kind
	if got := zero.JSONRPCCode(); got != cascade.RPCCodeInternal {
		t.Errorf("invalid Kind.JSONRPCCode() = %d, want RPCCodeInternal (%d) fallback", got, cascade.RPCCodeInternal)
	}
}

// TestTaxonomyTablesTotalAndNonOverlapping is the AC's explicit totality
// test: every kind maps to exactly one exit code and one JSON-RPC code, and
// within each table no two kinds share a code.
func TestTaxonomyTablesTotalAndNonOverlapping(t *testing.T) {
	kinds := cascade.AllKinds()
	if len(kinds) != 14 {
		t.Fatalf("AllKinds() = %d members, want 14", len(kinds))
	}

	exitSeen := make(map[int]cascade.Kind, len(kinds))
	rpcSeen := make(map[int]cascade.Kind, len(kinds))
	for _, k := range kinds {
		ec := k.ExitCode()
		if owner, dup := exitSeen[ec]; dup {
			t.Errorf("exit code %d is shared by %v and %v (non-overlap violated)", ec, owner, k)
		}
		exitSeen[ec] = k

		rc := k.JSONRPCCode()
		if owner, dup := rpcSeen[rc]; dup {
			t.Errorf("JSON-RPC code %d is shared by %v and %v (non-overlap violated)", rc, owner, k)
		}
		rpcSeen[rc] = k

		// Plugin RPC reuses the JSON-RPC table verbatim (A-T7 HOW).
		if got := cascade.NewPluginRPCError(cascade.New(k, "x")).Code; got != rc {
			t.Errorf("plugin RPC code for %v = %d, want %d (verbatim reuse of the JSON-RPC table)", k, got, rc)
		}
	}
	if len(exitSeen) != 14 {
		t.Errorf("exit code table covers %d distinct codes, want 14 (total)", len(exitSeen))
	}
	if len(rpcSeen) != 14 {
		t.Errorf("JSON-RPC code table covers %d distinct codes, want 14 (total)", len(rpcSeen))
	}
}

func TestKindFromExitCode_RoundTrip(t *testing.T) {
	for _, k := range cascade.AllKinds() {
		got, ok := cascade.KindFromExitCode(k.ExitCode())
		if !ok || got != k {
			t.Errorf("KindFromExitCode(%d) = (%v, %v), want (%v, true)", k.ExitCode(), got, ok, k)
		}
	}
	if _, ok := cascade.KindFromExitCode(cascade.ExitOK); ok {
		t.Error("KindFromExitCode(ExitOK) should be (_, false): 0 is success, not a Kind")
	}
	if _, ok := cascade.KindFromExitCode(999); ok {
		t.Error("KindFromExitCode(999) should be (_, false): not an assigned code")
	}
}

func TestKindFromJSONRPCCode_RoundTrip(t *testing.T) {
	for _, k := range cascade.AllKinds() {
		got, ok := cascade.KindFromJSONRPCCode(k.JSONRPCCode())
		if !ok || got != k {
			t.Errorf("KindFromJSONRPCCode(%d) = (%v, %v), want (%v, true)", k.JSONRPCCode(), got, ok, k)
		}
	}
	if _, ok := cascade.KindFromJSONRPCCode(-32601); ok {
		t.Error("KindFromJSONRPCCode(-32601) [spec method-not-found] should be (_, false)")
	}
}

func TestExitCode_TopLevelFunc(t *testing.T) {
	if got := cascade.ExitCode(nil); got != cascade.ExitOK {
		t.Errorf("ExitCode(nil) = %d, want ExitOK (%d)", got, cascade.ExitOK)
	}

	taxErr := cascade.New(cascade.KindNotFound, "missing")
	if got := cascade.ExitCode(taxErr); got != cascade.ExitNotFound {
		t.Errorf("ExitCode(taxonomy not-found) = %d, want %d", got, cascade.ExitNotFound)
	}

	canceled := cascade.New(cascade.KindCanceled, "sigint")
	if got := cascade.ExitCode(canceled); got != 130 {
		t.Errorf("ExitCode(canceled) = %d, want 130 (SIGINT convention)", got)
	}

	// Error path: a non-taxonomy error must still get a sane, documented
	// fallback rather than panicking or silently reporting success.
	plain := errors.New("boundary lint should have caught this upstream")
	if got := cascade.ExitCode(plain); got != cascade.ExitInternal {
		t.Errorf("ExitCode(non-taxonomy error) = %d, want ExitInternal (%d) fallback", got, cascade.ExitInternal)
	}
}

func TestNewRPCError(t *testing.T) {
	if got := cascade.NewRPCError(nil); got != nil {
		t.Errorf("NewRPCError(nil) = %+v, want nil", got)
	}

	taxErr := cascade.Newf(cascade.KindConflict, "revision %d superseded", 7)
	env := cascade.NewRPCError(taxErr)
	if env.Code != cascade.RPCCodeConflict {
		t.Errorf("Code = %d, want %d", env.Code, cascade.RPCCodeConflict)
	}
	if env.Message != taxErr.Error() {
		t.Errorf("Message = %q, want %q", env.Message, taxErr.Error())
	}
	if env.Error() != env.Message {
		t.Errorf("(*RPCError).Error() = %q, want Message %q", env.Error(), env.Message)
	}

	// Error path: a non-taxonomy error at the wire boundary still produces
	// a usable envelope instead of a nil-pointer panic.
	plain := errors.New("leaked past the boundary lint")
	fallback := cascade.NewRPCError(plain)
	if fallback.Code != cascade.RPCCodeInternal {
		t.Errorf("fallback Code = %d, want RPCCodeInternal (%d)", fallback.Code, cascade.RPCCodeInternal)
	}
	if fallback.Message != plain.Error() {
		t.Errorf("fallback Message = %q, want %q", fallback.Message, plain.Error())
	}
}

func TestNewPluginRPCError_MatchesJSONRPC(t *testing.T) {
	taxErr := cascade.New(cascade.KindUnsupported, "wasm ABI v3 not supported")
	rpc := cascade.NewRPCError(taxErr)
	plugin := cascade.NewPluginRPCError(taxErr)
	if plugin.Code != rpc.Code || plugin.Message != rpc.Message {
		t.Errorf("plugin RPC error %+v does not match JSON-RPC error %+v", plugin, rpc)
	}
}

// ExampleExitCode is a runnable godoc example for ExitCode, the entry point
// cmd/ composition roots call at the process boundary (Art.10.6).
func ExampleExitCode() {
	err := cascade.New(cascade.KindPermissionDenied, "vault locked")
	fmt.Println(cascade.ExitCode(err))
	// Output: 7
}
