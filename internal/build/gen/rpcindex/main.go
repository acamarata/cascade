// Command rpcindex regenerates docs/reference/rpc-methods.md. Run it with
// `go run ./internal/build/gen/rpcindex` from inside the repo; on any
// finding it exits non-zero and writes nothing.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/output"
)

func main() {
	os.Exit(run(warnWriter{output.NewDefault(false, false, false, false)}))
}

// warnWriter routes the generator's diagnostics through the output writer's
// warning channel; the output gate forbids writing to os.Stderr directly.
type warnWriter struct{ w *output.Writer }

func (ww warnWriter) Write(p []byte) (int, error) {
	ww.w.Warn("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// run regenerates the page from the working directory's repo and returns the
// process exit code.
func run(stderr io.Writer) int {
	root, err := findRoot()
	if err == nil {
		err = generate(root, filepath.Join(root, pagePath))
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "rpcindex:", err)
		return 1
	}
	return 0
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above the working directory")
		}
		dir = parent
	}
}
