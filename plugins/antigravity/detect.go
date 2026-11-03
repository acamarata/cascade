package antigravity

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// lookPath is injectable so detection never requires an installed CLI in tests.
var lookPath = defaultLookPath

// Detection reports presence and the resolved executable path.
type Detection struct {
	Present bool
	Path    string
}

// Detect resolves agy in PATH. Only a not-found error means absent; other
// lookup failures are returned with their original cause preserved.
func Detect() (Detection, error) {
	path, err := lookPath("agy")
	if err == nil {
		return Detection{Present: true, Path: path}, nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, cascade.ErrNotFound) {
		return Detection{}, nil
	}
	kind := cascade.KindInternal
	if errors.Is(err, os.ErrPermission) || errors.Is(err, cascade.ErrPermissionDenied) {
		kind = cascade.KindPermissionDenied
	}
	return Detection{}, cascade.Wrap(kind, err, "antigravity: locate agy")
}

// defaultLookPath follows the OpenCode filesystem-only PATH probe pattern.
// It avoids os/exec, which is restricted to the host process boundary, and
// retains filesystem failures rather than interpreting them as absence.
func defaultLookPath(name string) (string, error) {
	var failure error
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, candidate := range candidateNames(name) {
			path := filepath.Join(dir, candidate)
			info, err := os.Stat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				failure = err
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				failure = os.ErrPermission
				continue
			}
			return path, nil
		}
	}
	if failure != nil {
		return "", failure
	}
	return "", cascade.Newf(cascade.KindNotFound, "%s: executable file not found in PATH", name)
}

func candidateNames(name string) []string {
	if runtime.GOOS != "windows" {
		return []string{name}
	}
	var names []string
	for _, ext := range strings.Split(os.Getenv("PATHEXT"), string(filepath.ListSeparator)) {
		if ext != "" {
			names = append(names, name+ext)
		}
	}
	return append(names, name)
}
