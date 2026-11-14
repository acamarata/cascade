package mod

import (
	"errors"
	"fmt"
	"github.com/acamarata/cascade/internal/runtime"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrMissingSource indicates the repository CLAUDE.md file does not exist.
var ErrMissingSource = errors.New("source CLAUDE.md missing")

// ErrMountFailed indicates that mounting the CLAUDE.md file into a thread directory failed.
var ErrMountFailed = errors.New("mount CLAUDE.md failed")

// MountClaudeMD copies the content of repoFile into each directory in threadDirs as CLAUDE.md.
// It returns ErrMissingSource if repoFile does not exist.
// It returns an error wrapping ErrMountFailed naming the directory if a thread directory is missing.
func MountClaudeMD(repoFile string, threadDirs []string) error {
	data, err := os.ReadFile(repoFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrMissingSource
		}
		return fmt.Errorf("read source %s: %w", repoFile, err)
	}

	for _, dir := range threadDirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("%w: %s", ErrMountFailed, dir)
		}

		target := filepath.Join(dir, "CLAUDE.md")
		if err := runtime.WriteFileAtomic(target, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
	}
	return nil
}
