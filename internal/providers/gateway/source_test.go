// Purpose: unit tests for the gateway token source (env and helper).
// Constraints: no network, no vault, no real credentials. Tests use
// t.TempDir() for fake helpers and inject custody per P1-SEC-05 seam.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSourceTokenEnv(t *testing.T) {
	s := Source{Getenv: func(string) string { return "" }}
	a := Account{Name: "alpha", Credential: Credential{Kind: CredentialEnv, EnvVar: "ALPHA_KEY"}}
	_, err := s.Token(context.Background(), a)
	if err == nil {
		t.Fatal("expected error for empty env var")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("expected KindUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "ALPHA_KEY") {
		t.Fatalf("error should name the variable: %v", err)
	}
}

func TestSourceEnvNeverCaches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var canaries [2][24]byte
	for i := range canaries {
		if _, err := rand.Read(canaries[i][:]); err != nil {
			t.Fatal(err)
		}
	}
	callCount := 0
	s := Source{
		Getenv: func(k string) string {
			if k != "ALPHA_KEY" {
				t.Fatal("unexpected variable name")
			}
			value := hex.EncodeToString(canaries[callCount][:])
			callCount++
			return value
		},
	}
	a := Account{
		Name:       "alpha",
		Credential: Credential{Kind: CredentialEnv, EnvVar: "ALPHA_KEY"},
	}
	tok1, err := s.Token(context.Background(), a)
	if err != nil {
		t.Fatalf("first token call failed: %v", err)
	}
	defer clear(tok1)
	tok2, err := s.Token(context.Background(), a)
	if err != nil {
		t.Fatalf("second token call failed: %v", err)
	}
	defer clear(tok2)
	if callCount != 2 {
		t.Fatalf("expected Getenv called twice (no cache), got %d", callCount)
	}
	if string(tok1) != hex.EncodeToString(canaries[0][:]) || string(tok2) != hex.EncodeToString(canaries[1][:]) {
		t.Fatal("rotated environment credential was not read on each call")
	}
}

func TestSourceDefaultsAndUnsupportedKind(t *testing.T) {
	t.Setenv("GATEWAY_TEST_PROCESS_KEY", "from-process-env")
	var s Source
	tok, err := s.Token(context.Background(), Account{Name: "alpha", Credential: Credential{Kind: CredentialEnv, EnvVar: "GATEWAY_TEST_PROCESS_KEY"}})
	if err != nil || string(tok) != "from-process-env" {
		t.Fatalf("nil Getenv did not read the process environment: %q, %v", tok, err)
	}
	clear(tok)
	if got := (Source{}).timeout(); got != defaultHelperTimeout {
		t.Fatalf("zero Timeout = %v, want the default %v", got, defaultHelperTimeout)
	}
	if got := (Source{Timeout: time.Second}).timeout(); got != time.Second {
		t.Fatalf("set Timeout = %v, want 1s", got)
	}
	_, err = s.Token(context.Background(), Account{Name: "alpha", Credential: Credential{Kind: "bogus"}})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("unsupported credential kind: %v, want invalid-input", err)
	}
}
