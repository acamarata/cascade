// Purpose: decode and validate an externally-carried portable import bundle
//
//	(06 §5.7) — the untar/allowlist/bounds layer import.go's ImportPortable
//	calls before anything is landed on a destination Target.
//
// Inputs: the decrypted+decompressed tar byte stream.
// Outputs: the decoded []bundleFile members, or a typed fail-closed error.
// Constraints: never panics on adversarial input; every entry name is
//
//	restricted to the repo layout's three prefixes or the exact vault
//	member name, `..`/absolute-path traversal refuses, a non-regular
//	entry refuses, and both entry count and per-entry size are bounded.
//	FuzzImportBundleDecode drives decodeImportBundle directly.
//
// SPORT: internal.backup.import/ADDED (P1-E19-W4-S42-T2); split from
//
//	import.go under the 300-line file cap (P1-E19-W4-S42-T3).

package backup

import (
	"archive/tar"
	"bytes"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrImportBundleTraversal reports an entry name outside the three known
// repo-layout prefixes, or one carrying a `..`/absolute path segment — a
// malicious or corrupt bundle, never extracted.
var ErrImportBundleTraversal = cascade.New(cascade.KindIntegrity,
	"backup: import bundle entry name is outside the repo layout")

// ErrImportBundleTooLarge reports a bundle whose entry count or a single
// entry's size exceeds this decoder's bounds — a defense against a
// hostile artifact exhausting memory during decode, not a real repo
// content limit.
var ErrImportBundleTooLarge = cascade.New(cascade.KindIntegrity,
	"backup: import bundle exceeds decoder bounds")

// ErrImportBundleMalformed reports a tar stream this decoder cannot parse
// or whose declared entry size does not match its actual body length.
var ErrImportBundleMalformed = cascade.New(cascade.KindIntegrity,
	"backup: import bundle is not a valid tar stream")

const (
	// maxImportEntries bounds decodeImportBundle's entry count.
	maxImportEntries = 1 << 16
	// maxImportEntrySize bounds any single entry's byte length (64 MiB) —
	// generous for a manifest or chunk, small enough that a hostile
	// header cannot claim to hold gigabytes.
	maxImportEntrySize = 64 << 20
)

// bundleFile is one decoded tar member: its layout-relative name and raw
// bytes, exactly as export.go's writeTarMember wrote it.
type bundleFile struct {
	Name string
	Data []byte
}

// decodeImportBundle is this ticket's own decoder of an externally-carried
// format (06 §5.7): it never panics on adversarial input, restricts every
// entry name to the repo layout's three prefixes (or the exact vault
// member name), refuses `..`/absolute-path traversal, refuses a non-regular
// entry (directory, symlink, hardlink), and bounds both entry count and
// per-entry size. FuzzImportBundleDecode drives this function directly.
func decodeImportBundle(tarBytes []byte) ([]bundleFile, error) {
	r := tar.NewReader(bytes.NewReader(tarBytes))
	var out []bundleFile
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, ErrImportBundleMalformed.Error())
		}
		if len(out) >= maxImportEntries {
			return nil, ErrImportBundleTooLarge
		}
		if err := validateBundleEntry(hdr); err != nil {
			return nil, err
		}
		data, err := readBundleEntry(r, hdr)
		if err != nil {
			return nil, err
		}
		out = append(out, bundleFile{Name: hdr.Name, Data: data})
	}
}

// validateBundleEntry enforces the layout allowlist, regular-file-only,
// and size-bound rules a single tar header must satisfy before this
// decoder ever reads its body.
func validateBundleEntry(hdr *tar.Header) error {
	if hdr.Typeflag != tar.TypeReg {
		return ErrImportBundleTraversal
	}
	if hdr.Size < 0 || hdr.Size > maxImportEntrySize {
		return ErrImportBundleTooLarge
	}
	if !validBundleName(hdr.Name) {
		return ErrImportBundleTraversal
	}
	return nil
}

// validBundleName reports whether name is one of the repo layout's three
// prefixes (config/, manifests/, objects/) or the exact vault member name
// — never an absolute path, never containing a `..` path segment.
func validBundleName(name string) bool {
	if name == "" || name[0] == '/' {
		return false
	}
	for _, seg := range splitPath(name) {
		if seg == ".." || seg == "." || seg == "" {
			return false
		}
	}
	switch {
	case name == vaultBundleName:
		return true
	case hasPrefix(name, repoConfigDir+"/"):
		return true
	case hasPrefix(name, repoManifestsDir+"/"):
		return true
	case hasPrefix(name, repoObjectsDir+"/"):
		return true
	default:
		return false
	}
}

// splitPath splits a `/`-separated tar entry name into segments, without
// importing path/filepath (whose OS-specific separator handling this
// pure-format check does not want).
func splitPath(name string) []string {
	var segs []string
	start := 0
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			segs = append(segs, name[start:i])
			start = i + 1
		}
	}
	segs = append(segs, name[start:])
	return segs
}

// hasPrefix reports whether s starts with prefix, avoiding a strings
// import for one call site.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// readBundleEntry reads exactly hdr.Size bytes from r, refusing
// (ErrImportBundleMalformed) on a short read — a declared size that does
// not match the actual body is itself a malformed/tampered bundle.
func readBundleEntry(r *tar.Reader, hdr *tar.Header) ([]byte, error) {
	data := make([]byte, hdr.Size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, ErrImportBundleMalformed
	}
	return data, nil
}
