package background

import (
	"log"
	"runtime/debug"
)

// Recover executes the provided job function and catches any panics.
// If a panic occurs, it logs the panic value and a stack trace using
// the standard log package, and returns nil.
// If the function completes normally, its return value is returned.
func Recover(job func() any) (result any) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic recovered: %v\n%s", r, debug.Stack())
			result = nil
		}
	}()
	return job()
}
