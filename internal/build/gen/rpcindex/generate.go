package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/runtime"
)

const pagePath = "docs/reference/rpc-methods.md"

// render computes the rows under root and returns the rendered page. Any
// finding is returned as an error and nothing is rendered.
func render(root string) ([]byte, error) {
	rows, findings, err := build.BuildRPCMethodRows(root)
	if err != nil {
		return nil, err
	}
	if len(findings) > 0 {
		details := make([]string, 0, len(findings))
		for _, f := range findings {
			details = append(details, f.Detail)
		}
		return nil, fmt.Errorf("%d findings; page not written:\n%s", len(findings), strings.Join(details, "\n"))
	}
	return build.RenderRPCMethodReference(rows), nil
}

// generate renders the page for root and writes it to dest.
func generate(root, dest string) error {
	b, err := render(root)
	if err != nil {
		return err
	}
	return runtime.WriteFileAtomic(dest, b, 0o644)
}

// matchesCommitted reports whether regeneration equals the committed page.
func matchesCommitted(root string) (bool, error) {
	b, err := render(root)
	if err != nil {
		return false, err
	}
	have, err := os.ReadFile(filepath.Join(root, pagePath))
	if err != nil {
		return false, err
	}
	return string(have) == string(b), nil
}
