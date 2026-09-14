package remote

// Purpose: FuzzRemoteHandshakeResponse (§5.7, R-21.266): the handshake-
//   response decoding path is a network-facing decoder over bytes from
//   an untrusted remote host, so it carries a fuzz target. Seed corpus at
//   testdata/fuzz/FuzzRemoteHandshakeResponse/ (package-local, per
//   R-21.266's "internal/testdata/fuzz/ form is struck").
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import "testing"

// FuzzRemoteHandshakeResponse fuzzes decodeHandshakeResponse (handshake.go):
// arbitrary bytes from a remote host must never panic this process,
// however malformed, truncated, or adversarial. A 10s smoke run is the
// contract's own check (`go test -run='^$' -fuzz=... -fuzztime=10s`).
func FuzzRemoteHandshakeResponse(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{},
		[]byte(`{"jsonrpc":"2.0","id":1,"result":{"abi_version":1}}`),
		[]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"nope"}}`),
		[]byte(`{`),
		[]byte(`not json at all`),
		[]byte(`{"jsonrpc":"2.0","id":1}`),
		[]byte(`{"jsonrpc":"1.0","id":1,"result":{"abi_version":1}}`),
		[]byte(`{"result":{"abi_version":-1}}`),
		[]byte(`{"result":null,"error":null}`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decodeHandshakeResponse panicked on %q: %v", in, r)
			}
		}()
		_, _ = decodeHandshakeResponse(in)
	})
}
