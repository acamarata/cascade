package update

import (
	"archive/tar"
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// extractCase is one archive whose extraction must refuse.
type extractCase struct {
	name     string
	platform Platform
	members  func(version string) []member
	msg      string
}

var windowsPlatform = Platform{OS: "windows", Arch: "amd64"}

// withMember appends m after the default members, so the refusal comes
// after executables were already extracted.
func withMember(m member) func(string) []member {
	return func(v string) []member { return append(defaultMembers(v), m) }
}

func extractCases() []extractCase {
	return []extractCase{
		{name: "tar hard link", members: withMember(member{name: "hl", link: "cascade", typ: tar.TypeLink}), msg: `unsafe archive entry "hl"`},
		{name: "tar char device", members: withMember(member{name: "tty", typ: tar.TypeChar}), msg: `unsafe archive entry "tty"`},
		{name: "tar block device", members: withMember(member{name: "disk", typ: tar.TypeBlock}), msg: `unsafe archive entry "disk"`},
		{name: "tar fifo", members: withMember(member{name: "pipe", typ: tar.TypeFifo}), msg: `unsafe archive entry "pipe"`},
		{name: "tar duplicate member", members: withMember(member{name: "cascade", body: "evil"}),
			msg: `archive entry "cascade" is repeated`},
		{name: "tar backslash", members: withMember(member{name: `a\b`, body: "x"}), msg: `unsafe archive entry "a\\b"`},
		{name: "zip parent path", platform: windowsPlatform, members: withMember(member{name: "../x", body: "x"}), msg: `unsafe archive entry "../x"`},
		{name: "zip nested parent path", platform: windowsPlatform, members: withMember(member{name: "a/../../x", body: "x"}), msg: `unsafe archive entry "a/../../x"`},
		{name: "zip absolute", platform: windowsPlatform, members: withMember(member{name: "/abs", body: "x"}), msg: `unsafe archive entry "/abs"`},
		{name: "zip drive letter", platform: windowsPlatform, members: withMember(member{name: "C:/x", body: "x"}), msg: `unsafe archive entry "C:/x"`},
		{name: "zip symlink", platform: windowsPlatform, members: withMember(member{name: "link", link: "/etc/passwd"}), msg: `unsafe archive entry "link"`},
		{name: "zip backslash", platform: windowsPlatform, members: withMember(member{name: `..\x`, body: "x"}), msg: `unsafe archive entry "..\\x"`},
		{name: "zip duplicate member", platform: windowsPlatform, members: withMember(member{name: "cascade", body: "evil"}),
			msg: `archive entry "cascade" is repeated`},
	}
}

// fetchCase writes a signed dist for tc and runs FetchVerified on it.
func fetchCase(t *testing.T, platform Platform, members []member) (string, error) {
	t.Helper()
	key, dist, work := newTestKey(1), t.TempDir(), t.TempDir()
	if platform == (Platform{}) {
		platform = testPlatform
	}
	writeDist(t, dist, distSpec{version: "2.0.1", key: key, platform: platform, members: members})
	_, err := FetchVerified(context.Background(), DirFetcher{Dir: dist}, Source{Dist: dist}, "v2.0.1", platform, key.pub(t), work)
	return work, err
}

// TestExtractRefusesUnsafeMembers: hard links, devices, fifos, repeated
// members and every escaping zip name refuse with KindIntegrity and the
// member named, leaving no executable in workDir.
func TestExtractRefusesUnsafeMembers(t *testing.T) {
	for _, tc := range extractCases() {
		t.Run(tc.name, func(t *testing.T) {
			work, err := fetchCase(t, tc.platform, tc.members("2.0.1"))
			wantKind(t, err, cascade.KindIntegrity, tc.msg)
			if found := executablesUnder(t, work); len(found) != 0 {
				t.Fatalf("workDir holds executables after a refusal: %v", found)
			}
		})
	}
}

// TestExtractRefusesExpansionPastCap: an archive that expands past the cap
// refuses ("expands past"), tar and zip alike, leaving no executable.
func TestExtractRefusesExpansionPastCap(t *testing.T) {
	orig := maxExtractBytes
	t.Cleanup(func() { maxExtractBytes = orig })
	maxExtractBytes = 20
	for _, p := range []Platform{testPlatform, windowsPlatform} {
		work, err := fetchCase(t, p, defaultMembers("2.0.1"))
		wantKind(t, err, cascade.KindIntegrity, "the archive expands past 20 bytes")
		if found := executablesUnder(t, work); len(found) != 0 {
			t.Fatalf("%s: workDir holds executables after the cap refused: %v", p.OS, found)
		}
	}
	maxExtractBytes = orig
	if _, err := fetchCase(t, testPlatform, defaultMembers("2.0.1")); err != nil {
		t.Fatalf("the same archive under the real cap = %v", err)
	}
}
