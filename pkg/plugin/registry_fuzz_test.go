// Purpose: FuzzParseIndex exercises Ed25519Verifier.VerifyIndex's parse
//
//	path against arbitrary bytes. Seeds live at
//	internal/testdata/fuzz/registry-index/ (06-FORGE-SPEC §5.7's
//	"fuzz corpora live under internal/testdata/fuzz/, never beside the
//	package" — the same convention FuzzParseManifest already follows in
//	fuzz_test.go; the ticket's own files_scope names
//	pkg/plugin/registry/testdata/fuzz/FuzzParseIndex/seed001, which both
//	assumes the pkg/plugin/registry/ subpackage layout this ticket does
//	not use (see registry_client.go's header) and contradicts the
//	existing internal/testdata/fuzz/ convention; the tree's convention
//	wins, quoted here for the record).
//
// Constraints: no network calls; VerifyIndex must never panic on any
//
//	input, and must never return a non-zero RegistryIndex alongside a
//	non-nil error (fail-closed invariant).
//
// SPORT: pkg/plugin registry-client tests (ADD) — P1-E24-W5-S50-T1.
package plugin_test

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

const fuzzRegistryIndexCorpusDir = "../../internal/testdata/fuzz/registry-index"

func loadRegistryIndexFuzzSeeds(t *testing.F) []string {
	t.Helper()
	entries, err := os.ReadDir(fuzzRegistryIndexCorpusDir)
	if err != nil {
		t.Fatalf("reading fuzz corpus dir %s: %v", fuzzRegistryIndexCorpusDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	seeds := make([]string, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(fuzzRegistryIndexCorpusDir, name))
		if err != nil {
			t.Fatalf("reading fuzz seed %s: %v", name, err)
		}
		seeds = append(seeds, string(data))
	}
	if len(seeds) == 0 {
		t.Fatalf("fuzz corpus dir %s has no .json seeds", fuzzRegistryIndexCorpusDir)
	}
	return seeds
}

// FuzzParseIndex fuzzes Ed25519Verifier.VerifyIndex's JSON envelope parse
// path (06-FORGE-SPEC §5.7's FuzzXxx-for-parser requirement).
func FuzzParseIndex(f *testing.F) {
	for _, seed := range loadRegistryIndexFuzzSeeds(f) {
		f.Add(seed)
	}
	f.Add("")
	f.Add("{}")
	f.Add(`{"schema_version":"1"}`)
	f.Add(`{"schema_version":"1","entries":[],"signature":""}`)
	f.Add(`{"schema_version":"1","entries":"not-an-array","signature":"AA=="}`)

	verifier := realVerifierForFuzz()
	f.Fuzz(func(t *testing.T, data string) {
		idx, err := verifier.VerifyIndex(context.Background(), []byte(data))
		if err != nil {
			if !reflect.DeepEqual(idx, plugin.RegistryIndex{}) {
				t.Fatalf("VerifyIndex: got non-zero RegistryIndex %+v alongside error %v (fail-closed violation)", idx, err)
			}
		}
	})
}

// realVerifierForFuzz builds an Ed25519Verifier without a *testing.T
// (FuzzParseIndex's seed corpus does not carry a live public key for
// arbitrary fuzz input to verify against — every fuzzed input is
// expected to fail verification, since none of it is validly signed by
// this key; the point of the fuzz target is that VerifyIndex never
// panics and never leaks a partial result on the way to that failure).
func realVerifierForFuzz() plugin.Ed25519Verifier {
	raw, err := base64.StdEncoding.DecodeString(testRegistryPublicKeyB64)
	if err != nil {
		panic(err)
	}
	return plugin.Ed25519Verifier{PublicKey: raw}
}
