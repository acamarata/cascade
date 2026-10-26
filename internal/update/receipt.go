package update

// Purpose: read, validate and write the install receipt install.sh leaves
//   (contract:install-receipt), the only record of which files self-update
//   may replace.
// Inputs: the receipt path and its JSON bytes.
// Outputs: a validated Receipt, or a KindIntegrity refusal.
// Constraints: schema_version 1 only. A file name outside the fixed
//   executable set, any name with a path separator or "..", a relative
//   install_dir, or an install_dir that is a symlink (by Lstat) refuses.
//   Writes are atomic (runtime.WriteFileAtomic, mode 0644).
// SPORT: internal/update ReadReceipt/ADDED, WriteReceipt/ADDED.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// receiptSchemaVersion is the one schema this package reads and writes.
const receiptSchemaVersion = 1

// maxReceiptBytes bounds a receipt read; a real one is well under 2 KiB.
const maxReceiptBytes = 64 << 10

// Receipt is the install receipt (contract:install-receipt).
type Receipt struct {
	SchemaVersion int           `json:"schema_version"`
	Channel       string        `json:"channel"`
	ReleaseTag    string        `json:"release_tag"`
	ArchiveName   string        `json:"archive_name"`
	ArchiveSHA256 string        `json:"archive_sha256"`
	InstallDir    string        `json:"install_dir"`
	InstalledAt   string        `json:"installed_at"`
	Files         []ReceiptFile `json:"files"`
}

// ReceiptFile is one installed executable and its sha256.
type ReceiptFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// receiptGOOS decides whether receipt names carry ".exe".
var receiptGOOS = goruntime.GOOS

func receiptRefused(format string, args ...any) error {
	return cascade.Newf(cascade.KindIntegrity, "update: install receipt refused: "+format, args...)
}

// ReadReceipt reads and validates the receipt at path.
func ReadReceipt(path string) (Receipt, error) {
	f, err := os.Open(path)
	if err != nil {
		return Receipt{}, cascade.Wrapf(cascade.KindNotFound, err, "update: read install receipt %s", path)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
	if err != nil {
		return Receipt{}, cascade.Wrapf(cascade.KindUnavailable, err, "update: read install receipt %s", path)
	}
	r, err := parseReceipt(data)
	if err != nil {
		return Receipt{}, err
	}
	info, err := os.Lstat(r.InstallDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return Receipt{}, receiptRefused("install_dir %s is not a real directory", r.InstallDir)
	}
	return r, nil
}

// parseReceipt decodes and validates receipt bytes without touching disk.
func parseReceipt(data []byte) (Receipt, error) {
	if len(data) > maxReceiptBytes {
		return Receipt{}, receiptRefused("larger than %d bytes", maxReceiptBytes)
	}
	var r Receipt
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&r); err != nil {
		return Receipt{}, receiptRefused("not valid JSON")
	}
	if dec.More() {
		return Receipt{}, receiptRefused("trailing data after the JSON object")
	}
	if r.SchemaVersion != receiptSchemaVersion {
		return Receipt{}, receiptRefused("schema_version %d is not %d", r.SchemaVersion, receiptSchemaVersion)
	}
	if !buildinfo.ValidInstallChannels[r.Channel] {
		return Receipt{}, receiptRefused("unknown channel %q", r.Channel)
	}
	if !validTag(r.ReleaseTag) {
		return Receipt{}, receiptRefused("release_tag %q is not v + a SemVer version", r.ReleaseTag)
	}
	if !filepath.IsAbs(r.InstallDir) {
		return Receipt{}, receiptRefused("install_dir %q is not absolute", r.InstallDir)
	}
	if r.ArchiveSHA256 != "" && !isHexDigest(r.ArchiveSHA256) {
		return Receipt{}, receiptRefused("archive_sha256 is not a sha256 digest")
	}
	return r, validateReceiptFiles(r.Files)
}

// validateReceiptFiles enforces the fixed executable set, unique names and
// digest shape, and that cascade itself is listed.
func validateReceiptFiles(files []ReceiptFile) error {
	seen := map[string]bool{}
	for _, f := range files {
		if strings.ContainsAny(f.Name, `/\`) || strings.Contains(f.Name, "..") {
			return receiptRefused("file name %q is not a bare name", f.Name)
		}
		if !knownExecutable(f.Name, receiptGOOS) {
			return receiptRefused("file name %q is not a cascade executable", f.Name)
		}
		if seen[f.Name] {
			return receiptRefused("file name %q is listed twice", f.Name)
		}
		if !isHexDigest(f.SHA256) {
			return receiptRefused("file %q has no sha256 digest", f.Name)
		}
		seen[f.Name] = true
	}
	if !seen[exeName("cascade", receiptGOOS)] {
		return receiptRefused("files[] does not list %s", exeName("cascade", receiptGOOS))
	}
	return nil
}

// WriteReceipt validates r and writes it atomically at path (mode 0644).
func WriteReceipt(path string, r Receipt) error {
	data, err := json.Marshal(r)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "update: encode install receipt")
	}
	if _, err := parseReceipt(data); err != nil {
		return err
	}
	return runtime.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// isHexDigest reports a 64-character lowercase hex sha256.
func isHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// executableBases is the fixed set, in install order: helper and plugin
// first, cascade last.
var executableBases = []string{"cascade-elevate-helper", "cascade-github", "cascade"}

// exeName adds ".exe" for windows.
func exeName(base, goos string) string {
	if goos == "windows" {
		return base + ".exe"
	}
	return base
}

// knownExecutable reports whether name is one of the fixed set for goos.
func knownExecutable(name, goos string) bool {
	for _, b := range executableBases {
		if name == exeName(b, goos) {
			return true
		}
	}
	return false
}
