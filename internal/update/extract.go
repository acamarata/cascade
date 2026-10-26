package update

// Purpose: extract a digest-verified release archive (tar.gz or zip) into a
//   fresh private directory, refusing every entry that could escape it.
// Inputs: the archive path, its name (which decides the format), a target
//   directory that must not exist yet, the target OS (".exe" names).
// Outputs: the fixed-set executables found at the archive root, helper and
//   plugin first, cascade last.
// Constraints: refuses absolute names, any ".." segment, backslashes,
//   symlinks, hard links and every non-regular, non-directory entry; files
//   are created O_EXCL (a repeated name refuses) with mode 0600; total
//   extracted bytes are capped. The caller removes dir on any error.
// SPORT: internal/update extractArchive/ADDED.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxExtractBytes caps the total bytes extracted from one archive. A
// variable only so a test can lower it.
var maxExtractBytes int64 = 1 << 30

func unsafeEntry(name string) error {
	return integrity("unsafe archive entry %q", name)
}

// extractArchive extracts archivePath into dir and lists its executables.
func extractArchive(archivePath, name, dir, goos string) ([]string, error) {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "update: create %s", dir)
	}
	var err error
	switch {
	case strings.HasSuffix(name, ".tar.gz"):
		err = extractTarGz(archivePath, dir)
	case strings.HasSuffix(name, ".zip"):
		err = extractZip(archivePath, dir)
	default:
		err = integrity("archive %s has an unknown format", name)
	}
	if err != nil {
		return nil, err
	}
	var exes []string
	for _, base := range executableBases {
		n := exeName(base, goos)
		if info, err := os.Lstat(filepath.Join(dir, n)); err == nil && info.Mode().IsRegular() {
			exes = append(exes, n)
		}
	}
	if len(exes) == 0 || exes[len(exes)-1] != exeName("cascade", goos) {
		return nil, integrity("the archive carries no %s executable", exeName("cascade", goos))
	}
	return exes, nil
}

func extractTarGz(archivePath, dir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "update: open %s", archivePath)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return integrity("the archive is not gzip")
	}
	tr := tar.NewReader(gz)
	budget := maxExtractBytes
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return integrity("the archive is not a valid tar stream")
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = makeEntryDir(dir, h.Name)
		case tar.TypeReg:
			err = writeEntry(dir, h.Name, tr, &budget)
		default:
			err = unsafeEntry(h.Name)
		}
		if err != nil {
			return err
		}
	}
}

func extractZip(archivePath, dir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return integrity("the archive is not a valid zip")
	}
	defer func() { _ = zr.Close() }()
	budget := maxExtractBytes
	for _, zf := range zr.File {
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			err = makeEntryDir(dir, zf.Name)
		case mode.IsRegular():
			err = writeZipEntry(dir, zf, &budget)
		default:
			err = unsafeEntry(zf.Name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(dir string, zf *zip.File, budget *int64) error {
	rc, err := zf.Open()
	if err != nil {
		return integrity("archive entry %q does not open", zf.Name)
	}
	defer func() { _ = rc.Close() }()
	return writeEntry(dir, zf.Name, rc, budget)
}

// entryPath maps an archive name to a path under dir, or refuses it.
func entryPath(dir, name string) (string, error) {
	clean := strings.TrimSuffix(name, "/")
	if clean == "" || strings.HasPrefix(clean, "/") || strings.Contains(clean, `\`) || strings.Contains(clean, ":") {
		return "", unsafeEntry(name)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", unsafeEntry(name)
		}
	}
	if path.Clean(clean) != clean {
		return "", unsafeEntry(name)
	}
	return filepath.Join(dir, filepath.FromSlash(clean)), nil
}

func makeEntryDir(dir, name string) error {
	p, err := entryPath(dir, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p, 0o700); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "update: create %s", p)
	}
	return nil
}

// writeEntry writes one regular file, charging its bytes to budget.
func writeEntry(dir, name string, r io.Reader, budget *int64) error {
	p, err := entryPath(dir, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "update: create %s", filepath.Dir(p))
	}
	out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return integrity("archive entry %q is repeated or cannot be created", name)
	}
	n, err := io.Copy(out, io.LimitReader(r, *budget+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return integrity("archive entry %q does not extract", name)
	}
	*budget -= n
	if *budget < 0 {
		return integrity("the archive expands past %d bytes", maxExtractBytes)
	}
	return nil
}
