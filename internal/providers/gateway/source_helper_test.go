//go:build !windows

package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceNeverCaches(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	counter := filepath.Join(dir, "counter")
	p := filepath.Join(dir, "program")
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("#!/bin/sh\nn=0\nif [ -f '%s' ]; then read n < '%s'; fi\nn=$((n+1))\nprintf '%%s' \"$n\" > '%s'\nprintf '%%s' '%x'\n", counter, counter, counter, token)
	data := []byte(body)
	if err := os.WriteFile(p, data, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	a := Account{Name: "alpha", Credential: Credential{Kind: CredentialHelper, HelperPath: p, HelperSHA256: fmt.Sprintf("%x", sum)}}
	s := Source{Home: dir, User: "test-user"}
	for i := 0; i < 2; i++ {
		b, err := s.Token(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != fmt.Sprintf("%x", token) {
			t.Fatal("unexpected token")
		}
		clear(b)
	}
	count, err := os.ReadFile(counter)
	if err != nil || string(count) != "2" {
		t.Fatalf("helper not called twice: %v", err)
	}
}
