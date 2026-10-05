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
// Constraints: the implementation lives in atomic_write.go
//   (contract:atomic-file-write); writeBytesAtomic and WriteBytesAtomic
//   only delegate to it, so the temp-file-in-same-dir + rename pattern is
//   never reimplemented, which is precisely how blocking fix 2's Windows
//   path-separator bug (dirOf hardcoding '/') drifted into two
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

// writeBytesAtomic is the config tree's name for the one atomic-write
// primitive: WriteFileAtomic (atomic_write.go) with mode 0600, so a crash
// mid-write leaves either the untouched original or the new file, never a
// truncated config.toml (R-14.106 precedent). config_writer.go's
// ConfigWriter.Set/Unset and the other config write verbs call it.
func writeBytesAtomic(path string, data []byte) error {
	return WriteBytesAtomic(path, data)
}

// WriteBytesAtomic is WriteFileAtomic(path, data, 0o600): the owner-only
// atomic write its existing callers rely on. It also keeps the one thing
// it always did that WriteFileAtomic deliberately does not: it creates a
// missing parent directory (0700) first, which the config, trust-store and
// node-record callers depend on for a fresh home. Its signature and
// default mode are fixed; a caller that needs another mode calls
// WriteFileAtomic directly. The body is writeOwnerOnlyAtomic in
// atomic_write.go: this file only forwards.
func WriteBytesAtomic(path string, data []byte) error {
	return writeOwnerOnlyAtomic(path, data)
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
