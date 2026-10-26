package update

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestTrustedCommentVersionMismatchRefused is acceptance [12].
func TestTrustedCommentVersionMismatchRefused(t *testing.T) {
	key := newTestKey(1)
	for comment, msg := range map[string]string{
		"cascade 2.0.2 checksums": "version mismatch: the signature names 2.0.2",
		"cascade 2.0.1 checksum":  "version mismatch: the trusted comment is not of the form",
		"timestamp:1 file:x":      "version mismatch: the trusted comment is not of the form",
	} {
		dist, work := t.TempDir(), t.TempDir()
		writeDist(t, dist, distSpec{version: "2.0.1", key: key, comment: comment})
		_, err := FetchVerified(context.Background(), DirFetcher{Dir: dist}, Source{Dist: dist}, "v2.0.1", testPlatform, key.pub(t), work)
		wantKind(t, err, cascade.KindIntegrity, msg)
		if found := executablesUnder(t, work); len(found) != 0 {
			t.Fatalf("%q left executables: %v", comment, found)
		}
	}
}

// TestSelectsArchiveAmongSBOMLines is acceptance [15]: SBOM lines and
// other platforms never match; exactly the archive is selected.
func TestSelectsArchiveAmongSBOMLines(t *testing.T) {
	key, dist, work := newTestKey(1), t.TempDir(), t.TempDir()
	name := archiveNameFor("2.0.1", testPlatform)
	_, sum := writeDist(t, dist, distSpec{version: "2.0.1", key: key, extra: []string{
		hexSum([]byte("sbom")) + "  " + name + ".sbom.json",
		hexSum([]byte("darwin")) + "  cascade_2.0.1_darwin_arm64.tar.gz",
		hexSum([]byte("darwin sbom")) + "  cascade_2.0.1_darwin_arm64.tar.gz.sbom.json",
		hexSum([]byte("win")) + "  cascade_2.0.1_windows_amd64.zip",
	}})
	va, err := FetchVerified(context.Background(), DirFetcher{Dir: dist}, Source{Dist: dist}, "v2.0.1", testPlatform, key.pub(t), work)
	must(t, err)
	if va.ArchiveName != name || va.ArchiveSHA256 != sum {
		t.Fatalf("selected %s (%s), want %s", va.ArchiveName, va.ArchiveSHA256, name)
	}
}

// TestCompareVersions pins semver 2.0.0 precedence.
func TestCompareVersions(t *testing.T) {
	ordered := []string{"dev", "0.0.1", "2.0.0-alpha", "2.0.0-alpha.1", "2.0.0-alpha.beta", "2.0.0-beta.2", "2.0.0-beta.11",
		"2.0.0-rc.1", "2.0.0", "v2.0.1", "2.1.0", "10.0.0"}
	for i := 0; i+1 < len(ordered); i++ {
		if c, err := compareVersions(ordered[i], ordered[i+1]); err != nil || c != -1 {
			t.Errorf("compare(%s, %s) = %d, %v; want -1", ordered[i], ordered[i+1], c, err)
		}
		if c, err := compareVersions(ordered[i+1], ordered[i]); err != nil || c != 1 {
			t.Errorf("compare(%s, %s) = %d, %v; want 1", ordered[i+1], ordered[i], c, err)
		}
	}
	if c, err := compareVersions("2.0.0+build.1", "v2.0.0"); err != nil || c != 0 {
		t.Errorf("build metadata changed precedence: %d %v", c, err)
	}
	for _, bad := range []string{"2.0", "2.0.0.0", "02.0.0", "2.0.0-", "2.0.0-a..b", "x.y.z", ""} {
		if _, err := compareVersions(bad, "2.0.0"); err == nil {
			t.Errorf("compare accepted %q", bad)
		}
		if _, err := compareVersions("2.0.0", bad); err == nil {
			t.Errorf("compare accepted %q on the right", bad)
		}
	}
}

// FuzzChecksumsParse is acceptance [10]: no input panics, and an accepted
// file holds only well-formed, unique entries.
func FuzzChecksumsParse(f *testing.F) {
	f.Add([]byte(hexSum([]byte("a")) + "  cascade_2.0.1_linux_amd64.tar.gz\n" + hexSum([]byte("b")) + "  install.sh\n"))
	f.Add([]byte(hexSum([]byte("a")) + "  ../x\n"))
	f.Add([]byte("garbage"))
	f.Fuzz(func(t *testing.T, data []byte) {
		entries, err := parseChecksums(data)
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, e := range entries {
			if !isHexDigest(e.SHA256) || e.Name == "" || strings.ContainsAny(e.Name, `/\`) || seen[e.Name] {
				t.Fatalf("accepted bad entry %+v", e)
			}
			seen[e.Name] = true
		}
		_, _, _ = selectArchive(entries, testPlatform)
	})
}

// TestParseChecksumsRefusals covers the malformed-file refusals.
func TestParseChecksumsRefusals(t *testing.T) {
	h := hexSum([]byte("a"))
	for _, body := range []string{"", "\n", h + "\n", h + "  a b\n", "zz  a\n", h + "  dir/a\n", h + "  a\n" + h + "  a\n",
		h + "  *a\n", strings.Repeat("x", maxChecksumsBytes+1)} {
		if _, err := parseChecksums([]byte(body)); err == nil {
			t.Errorf("parseChecksums accepted %q", body[:min(len(body), 40)])
		}
	}
}
