package rpc

import (
	"fmt"
	"strconv"
	"strings"
)

// ProtocolVersion is this server's JSON-RPC wire protocol version. It is
// distinct from the daemon binary's release version (internal/buildinfo):
// this number changes only when the wire shape itself changes in a
// backward-incompatible way. Exported so D/S-07.T3's Go client SDK embeds
// the same constant rather than a second copy that could drift.
const ProtocolVersion = "1.0.0"

// ServerVersion is a package-level var (not a const) so a future ticket can
// wire it to internal/buildinfo.Version without changing this package's
// exported surface; it defaults to ProtocolVersion when unset by the
// daemon composition root.
var ServerVersion = ProtocolVersion

// ResponseEnvelope is the wrapper every JSON-RPC response carries, per this
// ticket's contract: {protocol_version, server_version} alongside the
// standard JSON-RPC 2.0 response members. Exported for D/S-07.T3's client
// SDK to decode directly.
type ResponseEnvelope struct {
	JSONRPC         string       `json:"jsonrpc"`
	ID              interface{}  `json:"id"`
	Result          any          `json:"result,omitempty"`
	Error           *ErrorObject `json:"error,omitempty"`
	ProtocolVersion string       `json:"protocol_version"`
	ServerVersion   string       `json:"server_version"`
}

// NewEnvelope builds a ResponseEnvelope carrying result (on success, error
// nil) or errObj (on failure, result nil) for the request identified by id.
// id is passed through as raw JSON (json.RawMessage or nil) so it
// round-trips byte-for-byte per the JSON-RPC 2.0 spec's id-echo
// requirement.
func NewEnvelope(id any, result any, errObj *ErrorObject) *ResponseEnvelope {
	return &ResponseEnvelope{
		JSONRPC:         jsonrpcVersion,
		ID:              id,
		Result:          result,
		Error:           errObj,
		ProtocolVersion: ProtocolVersion,
		ServerVersion:   ServerVersion,
	}
}

// ParseClientVersion splits a semantic-version-shaped string ("1.2.3" or
// "1.2.3-rc1") into its leading major-version integer. An empty string is
// reported via ok=false (the client omitted client_version — SkewCheck
// treats that as "no skew check possible, allow"); a non-empty string that
// does not parse as at least a leading integer major component is a
// KindInvalidInput condition the caller surfaces as codeInvalidRequest.
func ParseClientVersion(v string) (major int, ok bool) {
	if v == "" {
		return 0, false
	}
	firstDot := strings.IndexByte(v, '.')
	majorStr := v
	if firstDot >= 0 {
		majorStr = v[:firstDot]
	}
	n, err := strconv.Atoi(majorStr)
	if err != nil {
		return 0, false
	}
	return n, true
}

// protocolMajor returns ProtocolVersion's leading major-version integer.
// ProtocolVersion is a package constant under this package's own control,
// so a parse failure here is a programmer error, not client input; it
// panics rather than returning a wire error, matching Go's own
// regexp.MustCompile-style convention for invariants asserted at package
// scope.
func protocolMajor() int {
	major, ok := ParseClientVersion(ProtocolVersion)
	if !ok {
		panic("rpc: ProtocolVersion is not parseable as major.minor.patch: " + ProtocolVersion)
	}
	return major
}

// SkewCheck compares a request's client_version against the server's
// ProtocolVersion. A missing client_version is not a skew (older/looser
// clients that predate this field are allowed through); a client_version
// that fails to parse, or whose major version differs from the server's,
// returns a non-nil *ErrorObject at codeInvalidRequest (-32600) naming both
// versions in the message, per this ticket's AC ("message names both
// versions"). SkewCheck runs before Dispatch, so a skewed request never
// reaches the elevation middleware or a handler.
func SkewCheck(clientVersion string) *ErrorObject {
	if clientVersion == "" {
		return nil
	}
	clientMajor, ok := ParseClientVersion(clientVersion)
	if !ok {
		return newFramingError(codeInvalidRequest,
			fmt.Sprintf("invalid client_version %q: expected major.minor.patch", clientVersion))
	}
	serverMajor := protocolMajor()
	if clientMajor != serverMajor {
		return newFramingError(codeInvalidRequest,
			fmt.Sprintf("protocol version skew: client major version %d (client_version=%q) "+
				"does not match server major version %d (protocol_version=%q); upgrade the client",
				clientMajor, clientVersion, serverMajor, ProtocolVersion))
	}
	return nil
}
