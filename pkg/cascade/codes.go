package cascade

// This file holds the taxonomy's two frozen numeric code tables (T0 ruling
// R-14.3): CLI process exit codes and JSON-RPC application error codes.
// Plugin RPC reuses the JSON-RPC table verbatim (see wire.go). Both tables
// are total over AllKinds() and pairwise non-overlapping within themselves —
// asserted by TestTaxonomyTablesTotalAndNonOverlapping in wire_test.go.

// CLI exit codes. 0 (ok) is not a Kind — it is the absence of an error.
// ExitCanceled (130) follows the SIGINT convention (128 + signal 2) rather
// than continuing the low sequential numbering, matching how a shell reports
// a process killed by Ctrl-C.
const (
	// ExitOK is the process exit status for success (no error).
	ExitOK = 0
	// ExitInternal is KindInternal's exit status and the fallback status for
	// any error that does not carry a taxonomy Kind.
	ExitInternal = 1
	// ExitInvalidInput is KindInvalidInput's exit status.
	ExitInvalidInput = 2
	// ExitNotFound is KindNotFound's exit status.
	ExitNotFound = 3
	// ExitConflict is KindConflict's exit status.
	ExitConflict = 4
	// ExitUnavailable is KindUnavailable's exit status.
	ExitUnavailable = 5
	// ExitTimeout is KindTimeout's exit status.
	ExitTimeout = 6
	// ExitPermissionDenied is KindPermissionDenied's exit status.
	ExitPermissionDenied = 7
	// ExitElevationRequired is KindElevationRequired's exit status.
	ExitElevationRequired = 8
	// ExitPolicyDenied is KindPolicyDenied's exit status.
	ExitPolicyDenied = 9
	// ExitCapabilityDenied is KindCapabilityDenied's exit status.
	ExitCapabilityDenied = 10
	// ExitQuotaExhausted is KindQuotaExhausted's exit status.
	ExitQuotaExhausted = 11
	// ExitUnsupported is KindUnsupported's exit status.
	ExitUnsupported = 12
	// ExitIntegrity is KindIntegrity's exit status.
	ExitIntegrity = 13
	// ExitCanceled is KindCanceled's exit status, per the SIGINT convention
	// (128 + signal 2).
	ExitCanceled = 130
)

// JSON-RPC 2.0 application error codes. The JSON-RPC 2.0 spec reserves
// -32768..-32600 for protocol-defined errors (parse error, invalid request,
// method not found, invalid params, internal error) plus -32000..-32099 as
// an implementation-defined "server error" band; this taxonomy's codes sit
// at the top of that server-error band, -32013..-32000, entirely outside the
// spec-reserved range. Plugin RPC (wire.go) reuses this table verbatim.
const (
	// RPCCodeInternal is KindInternal's JSON-RPC error code.
	RPCCodeInternal = -32000
	// RPCCodeNotFound is KindNotFound's JSON-RPC error code.
	RPCCodeNotFound = -32001
	// RPCCodeInvalidInput is KindInvalidInput's JSON-RPC error code.
	RPCCodeInvalidInput = -32002
	// RPCCodeConflict is KindConflict's JSON-RPC error code.
	RPCCodeConflict = -32003
	// RPCCodeUnavailable is KindUnavailable's JSON-RPC error code.
	RPCCodeUnavailable = -32004
	// RPCCodeTimeout is KindTimeout's JSON-RPC error code.
	RPCCodeTimeout = -32005
	// RPCCodeCanceled is KindCanceled's JSON-RPC error code.
	RPCCodeCanceled = -32006
	// RPCCodePermissionDenied is KindPermissionDenied's JSON-RPC error code.
	RPCCodePermissionDenied = -32007
	// RPCCodeElevationRequired is KindElevationRequired's JSON-RPC error
	// code.
	RPCCodeElevationRequired = -32008
	// RPCCodePolicyDenied is KindPolicyDenied's JSON-RPC error code.
	RPCCodePolicyDenied = -32009
	// RPCCodeCapabilityDenied is KindCapabilityDenied's JSON-RPC error code.
	RPCCodeCapabilityDenied = -32010
	// RPCCodeQuotaExhausted is KindQuotaExhausted's JSON-RPC error code.
	RPCCodeQuotaExhausted = -32011
	// RPCCodeUnsupported is KindUnsupported's JSON-RPC error code.
	RPCCodeUnsupported = -32012
	// RPCCodeIntegrity is KindIntegrity's JSON-RPC error code.
	RPCCodeIntegrity = -32013
)

var exitCodeByKind = map[Kind]int{
	KindNotFound:          ExitNotFound,
	KindInvalidInput:      ExitInvalidInput,
	KindConflict:          ExitConflict,
	KindUnavailable:       ExitUnavailable,
	KindTimeout:           ExitTimeout,
	KindCanceled:          ExitCanceled,
	KindPermissionDenied:  ExitPermissionDenied,
	KindElevationRequired: ExitElevationRequired,
	KindPolicyDenied:      ExitPolicyDenied,
	KindCapabilityDenied:  ExitCapabilityDenied,
	KindQuotaExhausted:    ExitQuotaExhausted,
	KindUnsupported:       ExitUnsupported,
	KindIntegrity:         ExitIntegrity,
	KindInternal:          ExitInternal,
}

var rpcCodeByKind = map[Kind]int{
	KindNotFound:          RPCCodeNotFound,
	KindInvalidInput:      RPCCodeInvalidInput,
	KindConflict:          RPCCodeConflict,
	KindUnavailable:       RPCCodeUnavailable,
	KindTimeout:           RPCCodeTimeout,
	KindCanceled:          RPCCodeCanceled,
	KindPermissionDenied:  RPCCodePermissionDenied,
	KindElevationRequired: RPCCodeElevationRequired,
	KindPolicyDenied:      RPCCodePolicyDenied,
	KindCapabilityDenied:  RPCCodeCapabilityDenied,
	KindQuotaExhausted:    RPCCodeQuotaExhausted,
	KindUnsupported:       RPCCodeUnsupported,
	KindIntegrity:         RPCCodeIntegrity,
	KindInternal:          RPCCodeInternal,
}

var kindByExitCode = invertIntMap(exitCodeByKind)
var kindByRPCCode = invertIntMap(rpcCodeByKind)

func invertIntMap(m map[Kind]int) map[int]Kind {
	out := make(map[int]Kind, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

// ExitCode returns k's CLI process exit status. An invalid Kind (including
// the zero value) falls back to ExitInternal.
func (k Kind) ExitCode() int {
	if code, ok := exitCodeByKind[k]; ok {
		return code
	}
	return ExitInternal
}

// JSONRPCCode returns k's JSON-RPC application error code. An invalid Kind
// (including the zero value) falls back to RPCCodeInternal. Plugin RPC uses
// this same code (wire.go).
func (k Kind) JSONRPCCode() int {
	if code, ok := rpcCodeByKind[k]; ok {
		return code
	}
	return RPCCodeInternal
}

// KindFromExitCode reverse-looks-up the Kind for a CLI exit status produced
// by Kind.ExitCode. ok is false for ExitOK (which is not a Kind) or any
// status this taxonomy did not assign.
func KindFromExitCode(code int) (kind Kind, ok bool) {
	kind, ok = kindByExitCode[code]
	return kind, ok
}

// KindFromJSONRPCCode reverse-looks-up the Kind for a JSON-RPC application
// error code produced by Kind.JSONRPCCode. ok is false for any code this
// taxonomy did not assign.
func KindFromJSONRPCCode(code int) (kind Kind, ok bool) {
	kind, ok = kindByRPCCode[code]
	return kind, ok
}
