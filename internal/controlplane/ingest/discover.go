package ingest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidRoot is returned when a configured or environment plan root
// does not exist or is not a valid plan.
var ErrInvalidRoot = errors.New("ingest: invalid plan root")

// DiscoverPlanRoots returns the root paths for plans based on the provided
// configuration. It resolves roots from explicit configuration, environment
// variables, or default locations.
func DiscoverPlanRoots(cfg Config) ([]string, error) {
	var candidates []string
	isExplicit := false

	if len(cfg.Roots) > 0 {
		candidates = cfg.Roots
		isExplicit = true
	} else if cfg.Getenv != nil {
		env := cfg.Getenv("CAPP_PLAN_ROOTS")
		if env != "" {
			candidates = strings.Split(env, string(os.PathListSeparator))
			isExplicit = true
		}
	}

	if len(candidates) == 0 {
		candidates = []string{
			filepath.Join(cfg.Base, "hq"),
			filepath.Join(cfg.Base, ".ai"),
		}
		isExplicit = false
	}

	var roots []string
	for _, c := range candidates {
		if c == "" {
			if isExplicit {
				return nil, fmt.Errorf("%w: empty root path", ErrInvalidRoot)
			}
			continue
		}
		if isPlanRoot(c) {
			roots = append(roots, c)
		} else if isExplicit {
			return nil, fmt.Errorf("%w: %s", ErrInvalidRoot, c)
		}
	}
	return roots, nil
}

func isPlanRoot(root string) bool {
	phasesDir := filepath.Join(root, "phases")
	if _, err := OutlinePhase(phasesDir); err == nil {
		return true
	}
	currentYaml := filepath.Join(root, "current.yaml")
	if _, err := ParseCurrentYAML(currentYaml); err == nil {
		return true
	}
	return false
}
