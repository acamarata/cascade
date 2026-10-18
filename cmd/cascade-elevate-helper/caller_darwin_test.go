//go:build darwin && cgo

// Purpose: tests for the helper's caller check on a real Mac: the injected
//
//	decision logic, the real own-signature read (the test binary has no
//	Developer ID team, so it must refuse every caller), and the audit-token
//	peer resolution over a connection to a listener this process owns. No
//	Keychain call.
//
// SPORT: cmd/cascade-elevate-helper callerCheck tests/ADDED (P1-SHIP-15).
package main

import (
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestCallerCheckDecisions(t *testing.T) {
	good := func() (string, error) { return "ABCDE12345", nil }
	tok := []byte("0123456789abcdef0123456789abcdef")
	same := func(int) ([]byte, error) { return tok, nil }
	cases := map[string]struct {
		team   func() (string, error)
		token  func(int) ([]byte, error)
		peerOK bool
		wantOK bool
	}{
		"signed team and peer":   {good, same, true, true},
		"peer fails requirement": {good, same, false, false},
		"no team (unsigned)":     {func() (string, error) { return "", errCallerRefused() }, same, true, false},
		"malformed team":         {func() (string, error) { return "abc\" or true", nil }, same, true, false},
		"token changed since the connection (exec or another writer)": {good, func(int) ([]byte, error) {
			return []byte("ffffffffffffffffffffffffffffffff"), nil
		}, true, false},
		"token unreadable": {good, func(int) ([]byte, error) { return nil, errCallerRefused() }, true, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var gotReq string
			var gotHardened bool
			err := checkCaller(77, tok, c.team, c.token, func(_ []byte, req string, hardened bool) bool {
				gotReq, gotHardened = req, hardened
				return c.peerOK
			})
			if (err == nil) != c.wantOK {
				t.Fatalf("err = %v, want ok=%v", err, c.wantOK)
			}
			if err != nil && !cascade.HasKind(err, cascade.KindPermissionDenied) {
				t.Fatalf("refusal kind = %v", err)
			}
			if c.wantOK && (!gotHardened || !strings.Contains(gotReq, `"ABCDE12345"`) || !strings.Contains(gotReq, `identifier "cascade"`)) {
				t.Fatalf("peer checked hardened=%v with %q", gotHardened, gotReq)
			}
		})
	}
}

func TestCallerCheckRefusesUnsignedProcess(t *testing.T) {
	if team, err := selfTeam(); err == nil {
		t.Skipf("test binary carries team %q; the unsigned case needs an unsigned build", team)
	}
	client, _ := unixPair(t)
	check, err := bindCaller(fdOf(t, client))
	if err != nil {
		t.Fatal(err)
	}
	if err := check(); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("the bound caller check = %v, want permission-denied", err)
	}
	// With a team injected, the refusal can only come from the real
	// Security.framework judging this unsigned peer.
	fd := fdOf(t, client)
	first, err := peerToken(fd)
	if err != nil {
		t.Fatal(err)
	}
	team := func() (string, error) { return "ABCDE12345", nil }
	if err := checkCaller(fd, first, team, peerToken, tokenSatisfies); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("an unsigned peer judged against a team = %v, want permission-denied", err)
	}
}

// unixPair connects a client to a listener inside this process and returns
// the client end and the server end.
func unixPair(t *testing.T) (client, server *os.File) {
	t.Helper()
	accept, path := unixListener(t)
	accepted := make(chan *os.File, 1)
	go func() { accepted <- accept() }()
	c := unixDial(t, path)
	srv := <-accepted
	if srv == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() { _ = srv.Close() })
	return c, srv
}

func fdOf(t *testing.T, f *os.File) int {
	t.Helper()
	fd, err := connFD(f)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func TestPeerTokenResolvesTheListenerProcess(t *testing.T) {
	client, _ := unixPair(t)
	fd := fdOf(t, client)
	tok, err := peerToken(fd)
	if err != nil {
		t.Fatalf("peerToken: %v", err)
	}
	again, err := peerToken(fd)
	if err != nil || string(again) != string(tok) {
		t.Fatalf("two reads of an unchanged peer differ: %v", err)
	}
	if !tokenSatisfies(tok, `(anchor apple) or !(anchor apple)`, false) {
		t.Fatal("the peer of a connection to a listener this process owns was not resolved from its audit token")
	}
	if tokenSatisfies(tok, `(anchor apple) or !(anchor apple)`, true) {
		t.Fatal("a peer without the hardened runtime was accepted")
	}
	if tokenSatisfies(tok, `identifier "no.such.identifier.invalid"`, false) {
		t.Fatal("a requirement the peer does not meet was accepted")
	}
	if tokenSatisfies(tok, `this is not a requirement`, false) {
		t.Fatal("an unparsable requirement was accepted")
	}
	if tokenSatisfies(tok[:8], `(anchor apple) or !(anchor apple)`, false) {
		t.Fatal("a short token was accepted")
	}
	if _, err := peerToken(-1); err == nil {
		t.Fatal("a bad descriptor was accepted")
	}
}

func TestDarwinBackendIsUnavailableWithoutTheStorageEntitlement(t *testing.T) {
	b := newBackend()
	if !b.CGO() {
		t.Fatal("the cgo build must report cgo")
	}
	if hasStorageEntitlement() {
		t.Skip("test binary carries a storage entitlement")
	}
	// No Keychain access happens: the entitlement check short-circuits it.
	if b.Available() || b.Tier() != "unavailable" {
		t.Fatalf("available=%v tier=%s without the storage entitlement", b.Available(), b.Tier())
	}
}
