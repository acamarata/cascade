package promotion

import (
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// gateReportFile writes a release-gate report stand-in and returns its path.
func gateReportFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "gate-report.txt", "release gate: pass\n")
	return filepath.Join(dir, "gate-report.txt")
}

// signedSums signs sums for testVersion with k.
func signedSums(k testKey, sums []byte) []byte {
	return k.sign(sums, "cascade "+testVersion+" checksums")
}

// checkValidBuild asserts a valid signed set builds the expected record.
func checkValidBuild(t *testing.T, k testKey, files map[string]string, sums []byte, gate string) {
	t.Helper()
	rec, err := Build(sums, signedSums(k, sums), k.pub, testRC, testCommit, gate, testClock)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := slices.Sorted(maps.Keys(rec.Archives)); !slices.Equal(got, platforms) {
		t.Fatalf("archives keys %v, want %v", got, platforms)
	}
	wantAssets := []string{}
	for name := range files {
		if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".zip") {
			wantAssets = append(wantAssets, name)
		}
	}
	slices.Sort(wantAssets)
	if got := slices.Sorted(maps.Keys(rec.Assets)); len(wantAssets) != 7 || !slices.Equal(got, wantAssets) {
		t.Fatalf("assets %v, want every other line %v", got, wantAssets)
	}
	if rec.Archives["windows_amd64"] != digest([]byte(files[archiveFile(testVersion, "windows_amd64")])) {
		t.Fatal("windows archive digest is not the sha256 of its line")
	}
	if rec.FinalTag != testFinal || rec.PromotedAt != "2026-10-12T09:30:00Z" || rec.ChecksumsSHA256 != digest(sums) {
		t.Fatalf("record header wrong: %+v", rec)
	}
	if rec.GateReport.SHA256 != digest([]byte("release gate: pass\n")) || rec.GateReport.Path != gate {
		t.Fatalf("gate report wrong: %+v", rec.GateReport)
	}
}

func TestBuildRecordVerifiesSignatureFirst(t *testing.T) {
	k, other := newTestKey(t, 7), newTestKey(t, 9)
	gate := gateReportFile(t)
	files := testFiles()
	sums := checksumsFor(files)

	t.Run("valid set", func(t *testing.T) { checkValidBuild(t, k, files, sums, gate) })

	refusals := []struct {
		name, rcTag, commit, gate string
		sums, sig                 []byte
		kind                      cascade.Kind
		frag                      string
	}{
		{"bad signature (other key)", testRC, testCommit, gate, sums, other.sign(sums, "cascade "+testVersion+" checksums"), cascade.KindIntegrity, "does not verify"},
		{"bad signature (checksums edited)", testRC, testCommit, gate, flipFirstHex(sums), signedSums(k, sums), cascade.KindIntegrity, "does not verify"},
		{"signature checked before lines", testRC, testCommit, gate, append(append([]byte{}, sums...), "garbage\n"...), signedSums(k, sums), cascade.KindIntegrity, "does not verify"},
		{"garbage signature", testRC, testCommit, gate, sums, []byte("garbage\n"), cascade.KindIntegrity, "does not parse"},
		{"missing platform", testRC, testCommit, gate, withoutLine(sums, "windows_amd64.zip\n"), signedSums(k, withoutLine(sums, "windows_amd64.zip\n")), cascade.KindInvalidInput, "windows_amd64"},
		{"malformed line", testRC, testCommit, gate, append(append([]byte{}, sums...), "nothex  x\n"...), signedSums(k, append(append([]byte{}, sums...), "nothex  x\n"...)), cascade.KindInvalidInput, "malformed"},
		{"bad tag", "v2.1.0", testCommit, gate, sums, signedSums(k, sums), cascade.KindInvalidInput, "rc_tag"},
		{"comment names another version", "v2.1.1-rc.1", testCommit, gate, sums, signedSums(k, sums), cascade.KindIntegrity, "trusted comment"},
		{"bad commit", testRC, "abc", gate, sums, signedSums(k, sums), cascade.KindInvalidInput, "commit"},
		{"missing gate report", testRC, testCommit, filepath.Join(t.TempDir(), "absent"), sums, signedSums(k, sums), cascade.KindInvalidInput, "gate report"},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			rec, err := Build(c.sums, c.sig, k.pub, c.rcTag, c.commit, c.gate, testClock)
			wantRefusal(t, err, c.kind, c.frag)
			if !reflect.DeepEqual(rec, Record{}) {
				t.Fatalf("a refused Build returned a non-zero record: %+v", rec)
			}
		})
	}
}

// withoutLine drops the checksums line ending in suffix.
func withoutLine(sums []byte, suffix string) []byte {
	var kept []string
	for _, l := range strings.SplitAfter(string(sums), "\n") {
		if l != "" && !strings.HasSuffix(l, suffix) {
			kept = append(kept, l)
		}
	}
	return []byte(strings.Join(kept, ""))
}

// validRecord builds a complete record (with images) for Validate tests.
func validRecord(t *testing.T) Record {
	t.Helper()
	k := newTestKey(t, 7)
	sums := checksumsFor(testFiles())
	rec, err := Build(sums, signedSums(k, sums), k.pub, testRC, testCommit, gateReportFile(t), testClock)
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = WithImages(rec, []byte(testImages())); err != nil {
		t.Fatal(err)
	}
	if err = Validate(rec); err != nil {
		t.Fatalf("a built record does not validate: %v", err)
	}
	return rec
}

// flipFirstHex changes the first hex digit of the first checksums line.
func flipFirstHex(sums []byte) []byte {
	out := append([]byte{}, sums...)
	out[0] = map[bool]byte{true: '1', false: '0'}[out[0] == '0']
	return out
}
