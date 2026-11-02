package plugin

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the optional exec_sha256 table (P1-PLG-12): it parses, a
// malformed key or digest is refused, the per-platform lookup is typed, and
// a manifest without the table parses unchanged.
// SPORT: pkg/plugin manifest-v2-schema exec_sha256.

const execBaseManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=1.0.0"
runtime = "process"
requires = []
`

const execDigestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestManifestExecSHA256RoundTrip(t *testing.T) {
	bare, err := ParseManifest(strings.NewReader(execBaseManifest))
	if err != nil {
		t.Fatalf("a manifest without exec_sha256 must parse: %v", err)
	}
	if bare.ExecSHA256 != nil {
		t.Fatalf("ExecSHA256 = %v, want nil when absent", bare.ExecSHA256)
	}
	if _, err := bare.ExecDigestFor("linux", "amd64"); !cascade.HasKind(err, cascade.KindNotFound) ||
		!strings.Contains(err.Error(), "no signed executable digest for linux/amd64") {
		t.Fatalf("ExecDigestFor on an absent table = %v, want the typed not-found refusal", err)
	}
	withTable := execBaseManifest + "\n[exec_sha256]\n\"linux/amd64\" = \"" + execDigestA + "\"\n\"darwin/arm64\" = \"" + strings.Repeat("0f", 32) + "\"\n"
	m, err := ParseManifest(strings.NewReader(withTable))
	if err != nil {
		t.Fatalf("a manifest with exec_sha256 must parse: %v", err)
	}
	if got, err := m.ExecDigestFor("linux", "amd64"); err != nil || got != execDigestA {
		t.Fatalf("ExecDigestFor(linux/amd64) = %q, %v; want %q", got, err, execDigestA)
	}
	if _, err := m.ExecDigestFor("windows", "amd64"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("ExecDigestFor for an unlisted platform = %v, want KindNotFound", err)
	}
	bare.ExecSHA256 = nil
	m.ExecSHA256 = nil
	if m.ID != bare.ID || m.Version != bare.Version || m.Runtime != bare.Runtime {
		t.Fatalf("the table changed other fields: %+v vs %+v", m, bare)
	}
}

func TestManifestExecSHA256Validation(t *testing.T) {
	good := strings.Repeat("ab", 32)
	for name, table := range map[string]map[string]string{
		"uppercase hex":    {"linux/amd64": strings.ToUpper(good)},
		"short digest":     {"linux/amd64": good[:63]},
		"non hex":          {"linux/amd64": strings.Repeat("zz", 32)},
		"key without arch": {"linux": good},
		"key with space":   {"linux /amd64": good},
		"extra segment":    {"linux/amd64/x": good},
		"empty key":        {"": good},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateExecSHA256(table)
			if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "exec_sha256") {
				t.Fatalf("ValidateExecSHA256(%v) = %v, want KindInvalidInput naming exec_sha256", table, err)
			}
			if _, err := (Manifest{ExecSHA256: table}).ExecDigestFor("linux", "amd64"); !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("ExecDigestFor must validate the whole table first, got %v", err)
			}
		})
	}
	if err := ValidateExecSHA256(map[string]string{"linux/amd64": good, "linux/386": good}); err != nil {
		t.Fatalf("a valid table was refused: %v", err)
	}
}
