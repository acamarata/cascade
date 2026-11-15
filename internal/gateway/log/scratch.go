package log

import "os"

// RunWithScratch creates a temporary scratch directory under base, runs fn with
// the path to that directory, then removes the directory regardless of whether
// fn returns an error or panics. If fn panics, the panic is re-raised after
// cleanup. Returns the error from fn, or nil if fn succeeded.
func RunWithScratch(base string, fn func(string) error) (err error) {
	dir, mkErr := os.MkdirTemp(base, "scratch-")
	if mkErr != nil {
		return mkErr
	}
	defer func() {
		_ = os.RemoveAll(dir)
	}()

	defer func() {
		if r := recover(); r != nil {
			_ = os.RemoveAll(dir)
			panic(r)
		}
	}()

	return fn(dir)
}
