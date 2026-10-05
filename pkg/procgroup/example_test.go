// Purpose: a runnable godoc Example for procgroup.Signal: a group id that
//
//	kill(2) would misread is refused before any signal is sent.
//
// Constraints: the output is the same on every platform, so the Example
//
//	runs everywhere: unix refuses the id, windows refuses the call.
//
// SPORT: pkg/procgroup (TEST) — P1-PLG-09.
package procgroup_test

import (
	"fmt"
	"syscall"

	"github.com/acamarata/cascade/pkg/procgroup"
)

func ExampleSignal() {
	// Set cmd.SysProcAttr = procgroup.Attr() before Start so the child leads
	// its own group; then procgroup.Signal(cmd.Process.Pid, syscall.SIGTERM)
	// reaches the child and every grandchild. Group 0 would mean the
	// caller's own group, so it is refused.
	err := procgroup.Signal(0, syscall.SIGTERM)
	fmt.Println("refused:", err != nil)
	// Output: refused: true
}
