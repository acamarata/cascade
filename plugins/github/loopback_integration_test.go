//go:build integration

// Purpose: the loopback callback listener exercised against a REAL browser
//
//	redirect — an actual HTTP GET to the port it bound. Tagged `integration`
//	because it imports "net/http", which Art.7.2's no-network unit lane
//	forbids outright.
//
// It lives in package main because that is where Listen lives: the plugin's
// composition root owns every socket it opens, and the auth package it calls
// into owns the decisions. This file previously sat in package auth and
// silently stopped compiling when Listen moved — no CI lane built
// plugins/** with the integration tag, so nothing said so. The lane added in
// ci.yml alongside this move is what keeps that from recurring.
//
// SPORT: plugins/github tests (MOVE from plugins/github/auth) — P1-E25-W5-S51-T1.
package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/plugins/github/auth"
)

// TestLoopbackBindsLocalhostOnly is the exposure assertion: the callback
// endpoint must be reachable from this machine and nowhere else.
func TestLoopbackBindsLocalhostOnly(t *testing.T) {
	lb, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = lb.Close() }()

	uri := lb.RedirectURI()
	if !strings.HasPrefix(uri, "http://127.0.0.1:") {
		t.Fatalf("redirect uri = %q, want it bound to 127.0.0.1", uri)
	}
	if !strings.HasSuffix(uri, "/callback") {
		t.Errorf("redirect uri = %q, want the callback path", uri)
	}
}

// TestLoopbackAcceptsTheRedirectAndVerifiesState drives the real path: a
// real HTTP request to the real listener, verified against the real PKCE
// state.
func TestLoopbackAcceptsTheRedirectAndVerifiesState(t *testing.T) {
	lb, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = lb.Close() }()

	pkce, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}

	codes := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		code, err := lb.Wait(context.Background(), pkce)
		codes <- code
		errs <- err
	}()

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(
		lb.RedirectURI() + "?code=the-code&state=" + pkce.State)
	if err != nil {
		t.Fatalf("the callback request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the browser got %d; a human would see an error page", resp.StatusCode)
	}

	if err := <-errs; err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code := <-codes; code != "the-code" {
		t.Errorf("code = %q, want the one the redirect carried", code)
	}
}

// TestLoopbackRejectsAForgedState proves a callback delivered by anything
// else that can reach the port does not authorize this flow.
func TestLoopbackRejectsAForgedState(t *testing.T) {
	lb, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = lb.Close() }()

	pkce, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 1)
	go func() {
		_, err := lb.Wait(context.Background(), pkce)
		errs <- err
	}()

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(
		lb.RedirectURI() + "?code=the-code&state=forged")
	if err != nil {
		t.Fatalf("the callback request failed: %v", err)
	}
	_ = resp.Body.Close()

	if err := <-errs; err == nil {
		t.Fatal("a callback carrying the wrong state was accepted")
	}
}

// TestLoopbackWaitHonorsCancellation proves an abandoned consent screen
// ends the wait instead of holding the socket for the full five minutes.
func TestLoopbackWaitHonorsCancellation(t *testing.T) {
	lb, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = lb.Close() }()

	pkce, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := lb.Wait(ctx, pkce); err == nil {
		t.Fatal("Wait returned success after its context was cancelled")
	}
}

// TestLoopbackCloseIsIdempotent proves a double close (the flow completes,
// then the deferred close runs) is not an error.
func TestLoopbackCloseIsIdempotent(t *testing.T) {
	lb, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := lb.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := lb.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
