//go:build !(darwin && cgo)

// Purpose: a build without the Keychain says unavailable and refuses every
//
//	key op with kind unavailable, through the real caller check.
//
// SPORT: cmd/cascade-elevate-helper backend tests/ADDED (P1-SHIP-15).
package main

import "testing"

func TestHelperNoCGOBuildRefuses(t *testing.T) {
	caller, err := bindCaller(-1)
	if err != nil {
		t.Fatal(err)
	}
	s := server{be: newBackend(), caller: caller}
	if m := ask(t, s, `{"v":1,"op":"status"}`); m["ok"] != true || m["available"] != false || m["cgo"] != false || m["tier"] != "unavailable" {
		t.Fatalf("status = %v", m)
	}
	for _, line := range []string{`{"v":1,"op":"pubkey"}`, payloadReq([]byte("x")), `{"v":1,"op":"generate"}`, `{"v":1,"op":"delete"}`} {
		wantFailure(t, ask(t, s, line), "unavailable")
	}
	b := newBackend()
	if b.Presence("x") == nil || b.DeleteKey() == nil || b.Generate() == nil {
		t.Fatal("the unavailable backend let an operation through")
	}
	if _, err := b.Sign(nil, ""); err == nil {
		t.Fatal("sign succeeded without a keychain")
	}
}
