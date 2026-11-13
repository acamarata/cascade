// Package write provides control-plane write utilities.
package write

import (
	"fmt"
	"io"
)

// BaselineStore is the interface used to persist and retrieve baselines.
type BaselineStore interface {
	// Get returns the current baseline value.
	Get() (int, error)
	// Set stores a new baseline value.
	Set(n int) error
}

// Baselines is the package-level store used by RunGreens when --rebase is requested.
var Baselines BaselineStore

// RunGreens evaluates the current greens count against a baseline.
// When current < baseline it prints "dropped" to out and returns 1.
// When current >= baseline it returns 0.
// If args contains "--rebase", it sets the baseline to current via Baselines.Set and returns 0.
// Unknown flags cause a return value of 2.
func RunGreens(args []string, current, baseline int, out io.Writer) int {
	for _, arg := range args {
		if arg == "--rebase" {
			continue
		}
		if len(arg) > 0 && arg[0] == '-' {
			return 2
		}
	}

	rebase := false
	for _, arg := range args {
		if arg == "--rebase" {
			rebase = true
			break
		}
	}

	if rebase {
		if Baselines != nil {
			_ = Baselines.Set(current)
		}
		return 0
	}

	if current < baseline {
		fmt.Fprintln(out, "dropped")
		return 1
	}

	return 0
}
