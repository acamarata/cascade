package attest

import (
	"bytes"
	"testing"
)

// fuzzSeed is a well-formed canonical record (no signature needed: the
// fuzz target is the decoder every verifier runs first).
const fuzzSeed = `{"artifact_hashes":[],"attestor_class":"enrolled-node","checkpoint_id":"ckpt-1",` +
	`"commit_sha":"a1a1","ended_at":"2026-10-30T12:10:00Z","environment":{"ephemeral":false,` +
	`"isolation_class":"process","node_id":"n1","runner_kind":"local","toolchain_fp":"go1.26"},` +
	`"exit_status":0,"expires_at":"2026-10-31T00:00:00Z","job_spec_hash":"3c","key_id":"k1","node_id":"n1",` +
	`"nonce":"0f0f","policy_version":"policy-v1","repo_id":"repo-a","result_hashes":["lint:11"],` +
	`"schema_version":1,"started_at":"2026-10-30T12:00:00Z","target_selection":"all","toolchain_fp":"go1.26",` +
	`"tree_hash":"b2b2","oidc_token_sha256":"","oidc_run_id":""}`

// FuzzAttestationDecode: DecodeCanonical never panics, and whatever it
// accepts re-encodes to exactly the input bytes.
func FuzzAttestationDecode(f *testing.F) {
	f.Add([]byte(fuzzSeed))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema_version":2}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		rec, err := DecodeCanonical(data)
		if err != nil {
			if !IsSentinel(err, ErrInvalidRecord) {
				t.Fatalf("DecodeCanonical refused with %v, want ErrInvalidRecord", err)
			}
			return
		}
		again, err := CanonicalJSON(rec)
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("accepted bytes are not canonical: err=%v\nin  %q\nout %q", err, data, again)
		}
	})
}

func TestFuzzSeedIsCanonical(t *testing.T) {
	if _, err := DecodeCanonical([]byte(fuzzSeed)); err != nil {
		t.Fatalf("fuzz seed does not decode: %v", err)
	}
}
