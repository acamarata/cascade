package tripwire

import (
	"os"
	"path/filepath"
	"strings"
)

// ModuleRoot locates a source checkout from a working directory. The nearest
// go.mod is a module boundary: another module must never inherit this warning.
// Inputs: start directory. Outputs: absolute root and match. Constraints: local IO.
// SPORT: migration golden-tripwire contract; shared with sweep and harvester.
func ModuleRoot(startDir string) (string, bool) {
	if startDir == "" {
		return "", false
	}
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", false
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // caller's local module walk
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.TrimSpace(line) == "module github.com/acamarata/cascade" {
					return dir, true
				}
			}
			return "", false
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
