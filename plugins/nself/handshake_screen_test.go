// Purpose (this file): the LiteralScreener seam (config.go) under test,
//
//	and the package's TestMain, which binds the REAL value screen
//	(internal/runtime's ScreenConfigLiteral, the validator Set and
//	ApplyDiff share) exactly as internal/plugins' composition root does,
//	so every handshake test in this package screens with the real rules.
//
// Constraints: a test file may import internal/runtime (Art.10.2 covers
//
//	non-test files only; see handshake_apply_test.go).
//
// SPORT: plugins/nself handshake (TEST) — P1-PLG-01.

package nself

import (
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// runtimeScreener is the production binding's twin: one call into
// runtime.ScreenConfigLiteral.
type runtimeScreener struct{}

func (runtimeScreener) ScreenLiteral(path, literal string) error {
	return runtime.ScreenConfigLiteral(path, literal)
}

func TestMain(m *testing.M) {
	if err := SetLiteralScreener(runtimeScreener{}); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestSetLiteralScreenerRefusesNil(t *testing.T) {
	prev := literalScreener
	t.Cleanup(func() { literalScreener = prev })
	err := SetLiteralScreener(nil)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("SetLiteralScreener(nil) = %v, want KindInvalidInput", err)
	}
	if literalScreener != prev {
		t.Fatal("a refused nil screener replaced the bound one")
	}
}

// TestHandshakeRefusesWithoutScreener: with no screener bound the
// handshake refuses rather than proposing unscreened values, and forks
// nothing.
func TestHandshakeRefusesWithoutScreener(t *testing.T) {
	dir := nselfProjectDir(t)
	origRunner, prev := activeRunner, literalScreener
	script := fullScript()
	activeRunner, literalScreener = script, nil
	t.Cleanup(func() { activeRunner, literalScreener = origRunner, prev })
	withGetenv(t, true, "")

	for _, mode := range []handshakeMode{handshakeModePropose, handshakeModeApply} {
		resp, err := runHandshake(context.Background(), mode, dir)
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
			t.Fatalf("%s: runHandshake err = %v (resp %+v), want KindUnavailable", mode, err, resp)
		}
	}
	if len(script.calls) != 0 {
		t.Fatalf("an unscreened handshake forked nself: %v", script.calls)
	}
}
