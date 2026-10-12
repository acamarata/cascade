package promotion

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// recordFromDir builds the record of a signed set the way the tool does.
func recordFromDir(t *testing.T, k testKey, dir string) Record {
	t.Helper()
	rec, err := Build(readFile(t, dir, checksumsName), readFile(t, dir, signatureName), k.pub, testRC, testCommit, gateReportFile(t), testClock)
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = WithImages(rec, readFile(t, dir, imagesName)); err != nil {
		t.Fatal(err)
	}
	return rec
}

// stagingAndPublic returns the staging set of a signed dist and the public
// set (the same files without the unsigned cask).
func stagingAndPublic(t *testing.T, dir string) (map[string]string, map[string]string) {
	t.Helper()
	staging, err := SetFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	public := maps.Clone(staging)
	delete(public, caskName)
	if len(public) != len(staging)-1 || len(public) != 14 {
		t.Fatalf("fixture set sizes wrong: staging %d public %d", len(staging), len(public))
	}
	return staging, public
}

func TestVerifyIdenticalFailsOnOneByte(t *testing.T) {
	k := newTestKey(t, 7)
	dir := writeSignedSet(t, k)
	rec := recordFromDir(t, k, dir)
	staging, public := stagingAndPublic(t, dir)

	rep, err := VerifyIdentical(rec, staging, public)
	if err != nil || len(rep.Files) != 14 || rep.Images["cascade"] == "" {
		t.Fatalf("identical sets: %v, report %+v", err, rep)
	}
	sbom := archiveFile(testVersion, "linux_arm64") + ".sbom.json"
	flipped := digest([]byte("#!/bin/sh\necho instalm\n")) // one byte changed in install.sh
	cases := []struct {
		name, frag string
		edit       func(staging, public map[string]string)
	}{
		{"one byte in a public asset", "public install.sh has sha256", func(_, p map[string]string) { p["install.sh"] = flipped }},
		{"one byte in a staging archive", "staging " + archiveFile(testVersion, "darwin_arm64"), func(s, _ map[string]string) {
			s[archiveFile(testVersion, "darwin_arm64")] = flipped
		}},
		{"extra public file", "public has extra file notes.txt", func(_, p map[string]string) { p["notes.txt"] = flipped }},
		{"cask published", "public has extra file " + caskName, func(s, p map[string]string) { p[caskName] = s[caskName] }},
		{"extra staging file", "staging has extra file x.bin", func(s, _ map[string]string) { s["x.bin"] = flipped }},
		{"missing public file", "public is missing " + sbom, func(_, p map[string]string) { delete(p, sbom) }},
		{"missing signature", "public is missing " + signatureName, func(_, p map[string]string) { delete(p, signatureName) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, p := maps.Clone(staging), maps.Clone(public)
			c.edit(s, p)
			_, err := VerifyIdentical(rec, s, p)
			wantRefusal(t, err, cascade.KindIntegrity, c.frag)
		})
	}
	t.Run("nil set and invalid record", func(t *testing.T) {
		_, err := VerifyIdentical(rec, staging, nil)
		wantRefusal(t, err, cascade.KindInvalidInput, "both")
		bad := rec
		bad.SchemaVersion = 0
		_, err = VerifyIdentical(bad, staging, public)
		wantRefusal(t, err, cascade.KindInvalidInput, "schema_version")
	})
}

func TestSetFromDirRefusesSymlinkAndMissingDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a")
	if err := os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := SetFromDir(dir)
	wantRefusal(t, err, cascade.KindIntegrity, "link is not a regular file")
	_, err = SetFromDir(filepath.Join(dir, "absent"))
	wantRefusal(t, err, cascade.KindInvalidInput, "cannot read")
}
