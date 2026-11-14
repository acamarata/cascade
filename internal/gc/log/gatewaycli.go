package log

import (
	"fmt"
	"io"
)

// GatewaySupportMessage is the support message printed by GatewayCLI.
const GatewaySupportMessage = "Gateway is the GC door. For support, contact the owner.\n"

// GatewayUsage is the usage line printed for unknown subcommands.
const GatewayUsage = "usage: gateway\n"

// GatewayCLI handles gateway CLI invocations. When invoked with no args, it writes
// the support message to w and returns 0. With any args, it writes a usage
// line to w and returns 2.
func GatewayCLI(args []string, w io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(w, GatewaySupportMessage)
		return 0
	}
	_, _ = fmt.Fprint(w, GatewayUsage)
	return 2
}
