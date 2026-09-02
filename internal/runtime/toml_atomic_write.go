package runtime

// Purpose: the crash-safe atomic-write primitive behind every disk write
//   the structure-preserving TOML editor performs (config_writer.go's
//   ConfigWriter.Set/Unset, cmd/cascade/config's `edit` verb), plus
//   readOptionalFile, the matching "tolerate a not-yet-existing file"
//   read helper. Split out of toml_edit_scanner.go per R-14.117/Art.10.3
//   (300-line file cap) as part of this ticket's R-14 CR fix.
// Inputs: a target path and, for the write side, the bytes to write.
// Outputs: the read bytes (or nil for a missing file), or a crash-safe
//   write with disk left exactly as it was on any failure.
// Constraints: writeBytesAtomic is the ONLY atomic-write implementation
//   in this ticket's surface — cmd/cascade/config calls WriteBytesAtomic
//   rather than reimplementing the temp-file-in-same-dir + rename
//   pattern a second time, which is precisely how blocking fix 2's
//   Windows path-separator bug (dirOf hardcoding '/') drifted into two
//   independently-broken copies in the first place.
// SPORT: runtime/toml-edit-engine (ADD, placeholder per T-8 sport_updates).

import (
	"os"
	"path/filepath"
)

// readOptionalFile reads path, tolerating a not-yet-existing file as an
// empty document (matching config_load.go's Load: `cascade config set` on
// a fresh CASCADE_HOME with no config.toml yet creates one rather than
// erroring).
func readOptionalFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if os.IsNotExist(err) {
		return nil, nil
	}
	return nil, err
}

// writeBytesAtomic writes data to path via a temp-file-in-same-dir +
// rename, so a crash mid-write leaves either the untouched original or
// nothing at the temp path, never a truncated config.toml (R-14.106
// precedent). This is the ONE atomic-write primitive every write verb in
// this package uses (config_writer.go's ConfigWriter.Set/Unset); it is
// also exported as WriteBytesAtomic for cmd/cascade/config, so a second,
// divergent implementation is never written across the package boundary
// — see WriteBytesAtomic's doc comment (R-14 CR fix, P1-E03-W1-S05-T8,
// blocking fix 2).
func writeBytesAtomic(path string, data []byte) error {
	dir := dirOf(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// WriteBytesAtomic is writeBytesAtomic exported for cmd/cascade/config
// (internal/runtime's atomic-write primitive, reused rather than
// reimplemented — see writeBytesAtomic's doc comment).
func WriteBytesAtomic(path string, data []byte) error {
	return writeBytesAtomic(path, data)
}

// dirOf returns the directory portion of path via filepath.Dir.
//
// R-14 CR FINDING (P1-E03-W1-S05-T8, blocking fix 2): this used to be
// `strings.LastIndexByte(path, '/')`-based, hardcoding the Unix path
// separator even though every caller's path is built with filepath.Join.
// On Windows that made dirOf return "" for any real path (backslash
// separators, no '/' present at all), so writeBytesAtomic's temp file
// landed in os.TempDir() instead of path's own directory, and the final
// os.Rename crossed volumes — not atomic, and on Windows not even
// guaranteed to succeed (MoveFile-based rename can refuse a
// cross-volume move outright). cmd/cascade/config/config_write.go's
// writeConfigFile had the identical bug (same file split from this
// primitive, drifted independently); it has been deleted in favour of
// calling WriteBytesAtomic directly rather than re-diverging.
func dirOf(path string) string {
	return filepath.Dir(path)
}
