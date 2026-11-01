package runtime

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the config.toml permission policy (EPIC Decision 4). One
//   classifier (classifyConfigPerm, per-OS) feeds both Load, which refuses
//   or warns while reading, and CheckConfigPermissions, which the doctor
//   check mounts, so the two can never disagree (C5).
// Inputs: a config.toml path. A symlinked path is classified by the file
//   it resolves to and every ancestor of it and of each path component link.
// Outputs: a ConfigPermFinding; Load turns a refusal into a typed
//   KindPermissionDenied error naming the path, the mode or owner, and the
//   fix.
// Constraints: never chmods or rewrites the file. A missing file is fine
//   (Load never requires one). Refused: world-writable file, file owned by
//   neither root nor the running user, or any group/other-writable ancestor
//   unless root-owned and sticky. Foreign-owned ancestors also refuse.
//   Group-writable loads with one warning; world-readable is a
//   doctor-only warning. Windows reports not_checked.
// SPORT: runtime/config (ADD, P1-CORE-16).

// ConfigPermLevel is the severity of a ConfigPermFinding.
type ConfigPermLevel string

// The four levels CheckConfigPermissions reports.
const (
	ConfigPermOK         ConfigPermLevel = "ok"
	ConfigPermWarn       ConfigPermLevel = "warn"
	ConfigPermRefuse     ConfigPermLevel = "refuse"
	ConfigPermNotChecked ConfigPermLevel = "not_checked"
)

// ConfigPermFinding is the outcome of checking one config.toml.
type ConfigPermFinding struct {
	Level  ConfigPermLevel
	Reason string
	// loadWarns marks a warn-level finding that Load itself also reports
	// (group-writable does; world-readable is a doctor-only warning).
	loadWarns bool
}

// CheckConfigPermissions classifies path under the config permission
// policy without reading or changing it. The error is non-nil only when the
// file or its ancestors cannot be inspected, or the target is a directory.
func CheckConfigPermissions(path string) (ConfigPermFinding, error) {
	f, finding, err := openConfigChecked(path)
	if f != nil {
		_ = f.Close()
	}
	return finding, err
}

// configTarget is a config path and the file it resolves to.
type configTarget struct {
	path   string // as given
	real   string // after filepath.EvalSymlinks
	linked bool   // path itself is a symlink
}

// name renders the target for messages: the path, plus its target when
// the path is a symlink.
func (t configTarget) name() string {
	if t.linked {
		return t.path + " (a symlink to " + t.real + ")"
	}
	return t.path
}

// fixFile is the file a chmod/chown fix applies to.
func (t configTarget) fixFile() string {
	if t.linked {
		return t.real
	}
	return t.path
}

// openConfigChecked resolves path through any symlinks, opens the resolved
// file once and classifies that descriptor (mode and owner from fstat, then
// every ancestor of the resolved file and of each link on the way). Load
// reads from the returned file, so a swap after the check cannot change the
// bytes read. A target that is a directory is a KindInvalidInput error. The
// file is nil when there is no config file and when a dangling link is
// refused; the caller closes a non-nil file.
func openConfigChecked(path string) (*os.File, ConfigPermFinding, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, missingConfigFinding(path), nil
	}
	if err != nil {
		return nil, ConfigPermFinding{}, fmt.Errorf("runtime: resolve config %s: %w", path, err)
	}
	li, err := os.Lstat(path)
	if err != nil {
		return nil, ConfigPermFinding{}, fmt.Errorf("runtime: stat config %s: %w", path, err)
	}
	f, err := os.Open(resolved)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, missingConfigFinding(path), nil
	}
	if err != nil {
		return nil, ConfigPermFinding{}, fmt.Errorf("runtime: open config %s: %w", path, err)
	}
	t := configTarget{path: path, real: resolved, linked: li.Mode()&os.ModeSymlink != 0}
	fi, err := f.Stat()
	if err == nil && fi.IsDir() {
		err = cascade.New(cascade.KindInvalidInput, "runtime: config "+path+" resolves to a directory")
	}
	if err != nil {
		_ = f.Close()
		return nil, ConfigPermFinding{}, err
	}
	finding, err := classifyOpenConfig(f, t)
	if err != nil {
		_ = f.Close()
		return nil, ConfigPermFinding{}, err
	}
	return f, finding, nil
}

// missingConfigFinding distinguishes a missing file from a dangling path
// link. It Lstats the path's prefixes as written, left to right (never
// cleaned, so a later ".." cannot hide a link), and refuses when the
// deepest existing prefix is a symlink whose target is missing.
func missingConfigFinding(path string) ConfigPermFinding {
	deepest := ""
	for i := 1; i <= len(path); i++ {
		if i < len(path) && !os.IsPathSeparator(path[i]) {
			continue
		}
		prefix := path[:i]
		if os.IsPathSeparator(prefix[len(prefix)-1]) {
			continue // a repeated separator or the root itself
		}
		if _, err := os.Lstat(prefix); err != nil {
			break
		}
		deepest = prefix
	}
	if li, err := os.Lstat(deepest); deepest != "" && err == nil && li.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(deepest); errors.Is(err, fs.ErrNotExist) {
			target, _ := os.Readlink(deepest)
			return ConfigPermFinding{Level: ConfigPermRefuse, Reason: fmt.Sprintf(
				"%s is reached through dangling symlink %s to missing target %s; fix: repair the symlink", path, deepest, target)}
		}
	}
	return ConfigPermFinding{Level: ConfigPermOK, Reason: "no config file"}
}

// readConfigChecked applies the policy for Load and reads the file through
// the descriptor that was classified, via the same openConfigChecked the
// doctor check uses, so the two cannot disagree: a refusal becomes a typed
// KindPermissionDenied error, a group-writable file emits exactly one
// warning. found is false when there is no config file.
func readConfigChecked(path string, warn func(string, ...interface{})) (data []byte, found bool, err error) {
	f, finding, err := openConfigChecked(path)
	if err != nil {
		return nil, false, err
	}
	if f != nil {
		defer func() { _ = f.Close() }()
	}
	switch {
	case finding.Level == ConfigPermRefuse:
		return nil, false, cascade.New(cascade.KindPermissionDenied, "runtime: refusing to load config: "+finding.Reason)
	case finding.loadWarns && warn != nil:
		warn("runtime: %s", finding.Reason)
	}
	if f == nil {
		return nil, false, nil
	}
	if data, err = io.ReadAll(f); err != nil {
		return nil, false, fmt.Errorf("runtime: read config %s: %w", path, err)
	}
	return data, true, nil
}
