package update

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestCompareVersionsPrecedence: SemVer 2.0.0 precedence, including numeric
// identifiers longer than any machine integer.
func TestCompareVersionsPrecedence(t *testing.T) {
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "2.0.0-rc.1", "2.0.0", "2.0.1", "10.0.0",
		"99999999999999999999999.0.0",
	}
	for i := 0; i+1 < len(ordered); i++ {
		for _, pair := range [][2]string{{ordered[i], ordered[i+1]}, {ordered[i+1], ordered[i]}} {
			want := -1
			if pair[0] == ordered[i+1] {
				want = 1
			}
			got, err := compareVersions(pair[0], pair[1])
			if err != nil || got != want {
				t.Fatalf("compare(%s, %s) = %d, %v; want %d", pair[0], pair[1], got, err, want)
			}
		}
	}
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"2.0.0-9999999999999999999999", "2.0.0-100000000000000000000a", -1},
		{"2.0.0-100000000000000000000a", "2.0.0-9999999999999999999999", 1},
		{"2.0.0-10000000000000000000000", "2.0.0-9999999999999999999999", 1},
		{"2.0.0-18446744073709551616", "2.0.0-18446744073709551615", 1},
		{"1.0.0+build.1", "1.0.0", 0},
		{"v1.0.0+001", "1.0.0+002", 0},
		{"dev", "0.0.0-dev", 0},
		{"dev", "0.0.1", -1},
	} {
		got, err := compareVersions(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Fatalf("compare(%s, %s) = %d, %v; want %d", tc.a, tc.b, got, err, tc.want)
		}
	}
}

// TestParseSemverRefusesInvalidSyntax: every SemVer 2.0.0 syntax violation
// refuses with KindIntegrity naming the version.
func TestParseSemverRefusesInvalidSyntax(t *testing.T) {
	for _, v := range []string{
		"", "v", "1.0", "1.0.0.0", "01.0.0", "1.00.0", "1.0.00", "-1.0.0", "1.0.x",
		"1.0.0-", "1.0.0-01", "1.0.0-rc.01", "1.0.0-a..b", "1.0.0-a.", "1.0.0-a_b", "1.0.0-ü",
		"1.0.0+", "1.0.0+a..b", "1.0.0+a_b", "1.0.0-rc.1+", "1.0.0 ", "1.0.0-rc+b+c",
	} {
		_, err := parseSemver(v)
		wantKind(t, err, cascade.KindIntegrity, "is not semver")
	}
	for _, v := range []string{"1.0.0", "v1.0.0", "0.0.0", "1.0.0-0", "1.0.0-0a", "1.0.0-x-y.z", "1.0.0+001.sha-5114f85", "1.0.0-SNAPSHOT-abc123"} {
		if _, err := parseSemver(v); err != nil {
			t.Fatalf("parseSemver(%q) = %v, want valid", v, err)
		}
	}
}
