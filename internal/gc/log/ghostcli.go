package log

import (
	"fmt"
	"io"
)

// GhostSupportMessage is the support message printed by GhostCLI.
const GhostSupportMessage = "Ghost is the GC door. For support, contact the owner.\n"

// GhostUsage is the usage line printed for unknown subcommands.
const GhostUsage = "usage: ghost\n"

// GhostCLI handles Ghost CLI invocations. When invoked with no args, it writes
// the support message to w and returns 0. With any args, it writes a usage
// line to w and returns 2.
func GhostCLI(args []string, w io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(w, GhostSupportMessage)
		return 0
	}
	fmt.Fprint(w, GhostUsage)
	return 2
}
