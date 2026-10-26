package update

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// refusalCase is one tampered or ambiguous dist.
type refusalCase struct {
	name   string
	spec   func(*distSpec)
	tamper func(t *testing.T, dir, archive string)
	kind   cascade.Kind
	msg    string
}

// refusalCases is the acceptance [0] table.
func refusalCases() []refusalCase {
	return []refusalCase{
		{name: "garbage signature", tamper: func(t *testing.T, dir, _ string) {
			put(t, dir, checksumsName+".minisig", []byte("garbage\n"))
		}, kind: cascade.KindIntegrity, msg: "checksums signature does not verify"},
		{name: "signature by another key", spec: func(s *distSpec) { s.key = newTestKey(2) },
			kind: cascade.KindIntegrity, msg: "checksums signature does not verify"},
		{name: "checksums edited after signing", tamper: func(t *testing.T, dir, _ string) {
			b, err := os.ReadFile(filepath.Join(dir, checksumsName))
			must(t, err)
			put(t, dir, checksumsName, append(b, []byte(hexSum([]byte("x"))+"  extra.txt\n")...))
		}, kind: cascade.KindIntegrity, msg: "checksums signature does not verify"},
		{name: "archive byte flip", tamper: func(t *testing.T, dir, archive string) {
			b, err := os.ReadFile(filepath.Join(dir, archive))
			must(t, err)
			b[len(b)/2] ^= 0xff
			put(t, dir, archive, b)
		}, kind: cascade.KindIntegrity, msg: "sha256 mismatch"},
		{name: "two matching entries", spec: func(s *distSpec) {
			s.extra = []string{hexSum([]byte("other")) + "  cascade_2.0.1-x_linux_amd64.tar.gz"}
		}, kind: cascade.KindIntegrity, msg: "found 2 archive entries"},
		{name: "zero entries", spec: func(s *distSpec) { s.platform = Platform{OS: "darwin", Arch: "arm64"} },
			kind: cascade.KindIntegrity, msg: "found 0 archive entries"},
		{name: "parent path member", spec: func(s *distSpec) {
			s.members = append(defaultMembers(s.version), member{name: "../x", body: "x"})
		}, kind: cascade.KindIntegrity, msg: "unsafe archive entry"},
		{name: "absolute member", spec: func(s *distSpec) {
			s.members = append(defaultMembers(s.version), member{name: "/abs", body: "x"})
		}, kind: cascade.KindIntegrity, msg: "unsafe archive entry"},
		{name: "symlink member", spec: func(s *distSpec) {
			s.members = append(defaultMembers(s.version), member{name: "link", link: "/etc/passwd", typ: tar.TypeSymlink})
		}, kind: cascade.KindIntegrity, msg: "unsafe archive entry"},
	}
}

// TestFetchVerifiedRefuses is acceptance [0]: every tampered or ambiguous
// input refuses with a typed error and leaves no executable in workDir.
func TestFetchVerifiedRefuses(t *testing.T) {
	good := newTestKey(1)
	for _, tc := range refusalCases() {
		t.Run(tc.name, func(t *testing.T) {
			dist, work := t.TempDir(), t.TempDir()
			spec := distSpec{version: "2.0.1", key: good}
			if tc.spec != nil {
				tc.spec(&spec)
			}
			archive, _ := writeDist(t, dist, spec)
			if tc.tamper != nil {
				tc.tamper(t, dist, archive)
			}
			_, err := FetchVerified(context.Background(), DirFetcher{Dir: dist}, Source{Dist: dist},
				"v2.0.1", testPlatform, good.pub(t), work)
			wantKind(t, err, tc.kind, tc.msg)
			if found := executablesUnder(t, work); len(found) != 0 {
				t.Fatalf("workDir holds executables after a refusal: %v", found)
			}
		})
	}
}

// TestFetchVerifiedAccepts is the positive control for the refusal table.
func TestFetchVerifiedAccepts(t *testing.T) {
	key := newTestKey(1)
	dist, work := t.TempDir(), t.TempDir()
	archive, sum := writeDist(t, dist, distSpec{version: "2.0.1", key: key})
	va, err := FetchVerified(context.Background(), DirFetcher{Dir: dist}, Source{Dist: dist}, "v2.0.1", testPlatform, key.pub(t), work)
	must(t, err)
	if va.ArchiveName != archive || va.ArchiveSHA256 != sum || va.SignedVersion != "2.0.1" || va.ReleaseTag != "v2.0.1" {
		t.Fatalf("verified archive = %+v", va)
	}
	want := []string{"cascade-elevate-helper", "cascade-github", "cascade"}
	if strings.Join(va.Executables, ",") != strings.Join(want, ",") {
		t.Fatalf("executables = %v, want %v", va.Executables, want)
	}
	b, err := os.ReadFile(filepath.Join(va.Dir, "cascade"))
	must(t, err)
	if string(b) != "cascade 2.0.1" {
		t.Fatalf("extracted cascade = %q", b)
	}
}
