// Purpose: error and edge paths of the plugin.update reverify chain
// (reverify.go, update_rpc.go) that the acceptance tests do not reach: a
// missing store, a handler without a verifier, malformed and mismatched
// params, record states that cannot be reverified, index entries that do
// not vouch, and store or executable failures. Every refusal is checked
// for kind and message, and every record must stay byte-identical.
// SPORT: internal/plugins plugin reverify path tests (P1-PLG-12).
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// reverifyVia dispatches p, witnessed, through the middleware stack the
// daemon registers, over store and verify.
func reverifyVia(t *testing.T, store provider.Store, verify IndexVerifier, p any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	h := InstallMiddleware(nil, nil)(UpdateRPCMethod, WithReverify(UpdateRPCHandler(store, nil), store, verify))
	return attestedCall(t, UpdateRPCMethod, raw, h)
}

// stubVerifier accepts any bytes and returns idx.
func stubVerifier(idx plugin.RegistryIndex) IndexVerifier {
	return func(context.Context, []byte) (plugin.RegistryIndex, error) { return idx, nil }
}

// refusedUnchanged asserts err is kind with fragments and the record is
// unchanged.
func (f reverifyFixture) refusedUnchanged(t *testing.T, err error, kind cascade.Kind, fragments ...string) {
	t.Helper()
	requireRefusal(t, err, kind, fragments...)
	if after := storedRecordHash(t, f.store, "trustdemo"); after != f.before {
		t.Fatalf("a refused reverify changed the record: %s -> %s", f.before, after)
	}
}

func TestReverifyWithoutVerifierRefuses(t *testing.T) {
	f := newReverifyFixture(t)
	raw, err := json.Marshal(f.goodParams(t))
	if err != nil {
		t.Fatal(err)
	}
	// The bare update handler has no index verifier, so it must refuse.
	_, err = attestedCall(t, UpdateRPCMethod, raw, InstallMiddleware(nil, nil)(UpdateRPCMethod, UpdateRPCHandler(f.store, nil)))
	f.refusedUnchanged(t, err, cascade.KindPolicyDenied, "no signed registry index to verify against")
	p := f.goodParams(t)
	p.IndexBytes = nil
	_, err = reverifyVia(t, f.store, f.verifier(), p)
	f.refusedUnchanged(t, err, cascade.KindPolicyDenied, "no signed registry index to verify against")
}

func TestReverifyRejectsMalformedParams(t *testing.T) {
	f := newReverifyFixture(t)
	p := f.goodParams(t)
	p.Reverify = false // index bytes without the reverify flag
	_, err := reverifyVia(t, f.store, f.verifier(), p)
	f.refusedUnchanged(t, err, cascade.KindInvalidInput, "index_bytes is only accepted with reverify")
	extra := map[string]any{"id": "trustdemo", "reverify": true, "index_bytes": []byte("x"), "surprise": 1}
	_, err = reverifyVia(t, f.store, f.verifier(), extra)
	f.refusedUnchanged(t, err, cascade.KindInvalidInput, "decode params")
}

func TestReverifyRefusesForeignOrBrokenManifest(t *testing.T) {
	f := newReverifyFixture(t)
	broken := reverifyParams("id = [not toml", []byte("index"))
	_, err := reverifyVia(t, f.store, f.verifier(), broken)
	f.refusedUnchanged(t, err, cascade.KindPolicyDenied, "the manifest does not parse")
	other := strings.Replace(execManifest(f.newDigest), `id = "trustdemo"`, `id = "otherdemo"`, 1)
	_, err = reverifyVia(t, f.store, f.verifier(), reverifyParams(other, []byte("index")))
	f.refusedUnchanged(t, err, cascade.KindPolicyDenied, "not this plugin's process manifest")
}

// rewriteRecord stores edit(rec) for trustdemo and refreshes f.before.
func (f *reverifyFixture) rewriteRecord(t *testing.T, edit func(*PluginMetadata)) {
	t.Helper()
	rec, _, err := LoadMetadata(context.Background(), f.store, "trustdemo")
	if err != nil {
		t.Fatal(err)
	}
	edit(&rec)
	if err := SaveMetadata(context.Background(), f.store, rec); err != nil {
		t.Fatal(err)
	}
	f.before = storedRecordHash(t, f.store, "trustdemo")
}

func TestReverifyRefusesUnreverifiableRecords(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*PluginMetadata)
		want string
	}{
		"not a process record": {func(r *PluginMetadata) { r.RuntimeMode = plugin.RuntimeWasm }, "not a process plugin"},
		"held untrusted":       {func(r *PluginMetadata) { r.TrustTier = "" }, "held at the untrusted tier"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReverifyFixture(t)
			f.rewriteRecord(t, tc.edit)
			_, err := reverifyVia(t, f.store, f.verifier(), f.goodParams(t))
			f.refusedUnchanged(t, err, cascade.KindPolicyDenied, tc.want)
		})
	}
}

func TestReverifyRefusesUninstalledPlugin(t *testing.T) {
	f := newTrustFixture(t, "trustdemo")
	m := execManifest(f.digest)
	_, err := reverifyVia(t, f.store, stubVerifier(plugin.RegistryIndex{}), reverifyParams(m, []byte("index")))
	requireRefusal(t, err, cascade.KindPolicyDenied, "it is not installed")
	if got := storedRecordHash(t, f.store, "trustdemo"); got != "absent" {
		t.Fatalf("a refused reverify wrote a record: %s", got)
	}
}

func TestReverifyRefusesIndexWithoutEntry(t *testing.T) {
	f := newReverifyFixture(t)
	p := f.goodParams(t)
	for name, entries := range map[string][]plugin.RegistryIndexEntry{
		"no entries":    nil,
		"other id":      {{ID: "otherdemo", Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: p.Checksum}}}},
		"other version": {{ID: "trustdemo", Versions: []plugin.RegistryVersionEntry{{Version: "0.9.0", Checksum: p.Checksum}}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := reverifyVia(t, f.store, stubVerifier(plugin.RegistryIndex{Sequence: 3, Entries: entries}), p)
			f.refusedUnchanged(t, err, cascade.KindPolicyDenied, "the signed index has no entry for trustdemo v1.0.0")
		})
	}
	p.Checksum = ""
	idx := plugin.RegistryIndex{Sequence: 3, Entries: []plugin.RegistryIndexEntry{{ID: "trustdemo",
		Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0"}}}}}
	_, err := reverifyVia(t, f.store, stubVerifier(idx), p)
	f.refusedUnchanged(t, err, cascade.KindPolicyDenied, "not the checksum the call pins")
}

func TestReverifyRefusesMissingExecutable(t *testing.T) {
	f := newReverifyFixture(t)
	if err := os.Remove(f.exe); err != nil {
		t.Fatal(err)
	}
	_, err := reverifyVia(t, f.store, f.verifier(), f.goodParams(t))
	f.refusedUnchanged(t, err, cascade.KindPolicyDenied, "the executable is not trustable")
}

func TestReverifyStoreFailures(t *testing.T) {
	f := newReverifyFixture(t)
	for name, store := range map[string]failingStore{
		"load": {MemStore: f.store, failGet: true},
		"save": {MemStore: f.store, failPut: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := reverifyVia(t, store, f.verifier(), f.goodParams(t))
			if !errors.Is(err, errStoreFailure) {
				t.Fatalf("reverify over a failing store (%s) = %v, want the store error", name, err)
			}
			if after := storedRecordHash(t, f.store, "trustdemo"); after != f.before {
				t.Fatalf("a failed reverify changed the record: %s -> %s", f.before, after)
			}
		})
	}
}

func TestWithReverifyRefusesUnwitnessedCall(t *testing.T) {
	f := newReverifyFixture(t)
	raw, err := json.Marshal(f.goodParams(t))
	if err != nil {
		t.Fatal(err)
	}
	// Called directly, with no elevation middleware in front of it.
	_, err = WithReverify(UpdateRPCHandler(f.store, nil), f.store, f.verifier())(context.Background(), raw)
	f.refusedUnchanged(t, err, cascade.KindElevationRequired, "no verified elevation")
}

func TestReverifyTrustRefusesPlainUpdate(t *testing.T) {
	f := newReverifyFixture(t)
	p := f.goodParams(t)
	p.Reverify = false
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	handler := InstallMiddleware(nil, nil)(UpdateRPCMethod, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return reverifyTrust(ctx, f.store, f.verifier(), p)
	})
	_, err = attestedCall(t, UpdateRPCMethod, raw, handler)
	f.refusedUnchanged(t, err, cascade.KindPolicyDenied, "the call is not a reverify")
}
