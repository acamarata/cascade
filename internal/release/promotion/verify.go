package promotion

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Report lists what a verification checked: every file name it compared
// (sorted, slash-separated) and, for a signed set, the signed image digests.
type Report struct {
	Files  []string          `json:"files"`
	Images map[string]string `json:"images,omitempty"`
}

// SetFromDir reads every regular file under dir and returns its
// slash-separated relative name -> sha256 of the bytes read. A symlink or
// any other non-regular file refuses: a set is plain files only.
func SetFromDir(dir string) (map[string]string, error) {
	set := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return invalid("cannot read %s: %v", path, err)
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return invalid("cannot name %s under %s", path, dir)
		}
		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			return refused("%s is not a regular file", rel)
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return invalid("cannot read %s: %v", rel, readErr)
		}
		set[rel] = digest(b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

// expectedFiles maps every file a record covers to its recorded digest:
// the five archives by goreleaser name, every asset, the checksums file and
// its signature.
func expectedFiles(r Record) map[string]string {
	version := strings.TrimPrefix(r.FinalTag, "v")
	want := maps.Clone(r.Assets)
	for p, d := range r.Archives {
		want[archiveFile(version, p)] = d
	}
	want[checksumsName] = r.ChecksumsSHA256
	want[signatureName] = r.SignatureSHA256
	return want
}

// compareSet refuses, naming the file, when got lacks a wanted file, holds
// one with another digest, or holds any file beyond want. allowCask admits
// the unsigned Casks/cascade.rb (staging only; it is never published).
func compareSet(side string, want, got map[string]string, allowCask bool) error {
	for _, name := range slices.Sorted(maps.Keys(want)) {
		d, ok := got[name]
		if !ok {
			return refused("%s is missing %s", side, name)
		}
		if d != want[name] {
			return refused("%s %s has sha256 %s, want %s", side, name, d, want[name])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[name]; !ok && (!allowCask || name != caskName) {
			return refused("%s has extra file %s", side, name)
		}
	}
	return nil
}

// VerifyIdentical checks that every file the record covers is present in
// both the staging and the public set with the recorded digest, and that
// neither set holds anything else (staging may also hold the unsigned cask).
// Any missing, differing or extra file is a KindIntegrity error naming it.
func VerifyIdentical(record Record, staging, public map[string]string) (Report, error) {
	if err := Validate(record); err != nil {
		return Report{}, err
	}
	if staging == nil || public == nil {
		return Report{}, invalid("both a staging and a public set are required")
	}
	want := expectedFiles(record)
	if err := compareSet("staging", want, staging, true); err != nil {
		return Report{}, err
	}
	if err := compareSet("public", want, public, false); err != nil {
		return Report{}, err
	}
	return Report{Files: slices.Sorted(maps.Keys(want)), Images: maps.Clone(record.Images)}, nil
}
