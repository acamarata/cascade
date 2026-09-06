// Purpose: NaiveTokenCounter's contract tests, with particular attention to
//   multi-byte UTF-8: the estimate must count Unicode code points, never
//   bytes, or a non-ASCII script would be silently over-counted relative to
//   an equivalent-length ASCII string.
// SPORT: pkg.provider.NaiveTokenCounter/ADDED (P1-E05-W2-S09-T1).

package provider_test

import (
	"context"
	"testing"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestNaiveTokenCounterCountsRunesNotBytes(t *testing.T) {
	c := provider.NaiveTokenCounter{}
	// "文脈窓口ok" mixes CJK (3 bytes/rune in UTF-8) with ASCII. If the
	// estimate counted bytes it would report far more than if it counted
	// runes.
	text := "文脈窓口ok"
	wantRunes := utf8.RuneCountInString(text)
	if byteLen := len(text); byteLen == wantRunes {
		t.Fatalf("test fixture %q is not multi-byte (len=%d runes=%d); fixture must exercise multi-byte UTF-8", text, byteLen, wantRunes)
	}
	got, err := c.Count(context.Background(), text)
	if err != nil {
		t.Fatalf("Count: unexpected error %v", err)
	}
	if want := wantRunes / 4; got != want {
		t.Errorf("Count(%q) = %d, want %d (rune count %d / 4, not byte count %d / 4)",
			text, got, want, wantRunes, len(text))
	}
}

func TestNaiveTokenCounterEmptyString(t *testing.T) {
	c := provider.NaiveTokenCounter{}
	got, err := c.Count(context.Background(), "")
	if err != nil {
		t.Fatalf("Count: unexpected error %v", err)
	}
	if got != 0 {
		t.Errorf("Count(\"\") = %d, want 0", got)
	}
}

func TestNaiveTokenCounterASCII(t *testing.T) {
	c := provider.NaiveTokenCounter{}
	text := "0123456789ab" // 12 runes
	got, err := c.Count(context.Background(), text)
	if err != nil {
		t.Fatalf("Count: unexpected error %v", err)
	}
	if got != 3 {
		t.Errorf("Count(%q) = %d, want 3", text, got)
	}
}

func TestNaiveTokenCounterNeverErrors(t *testing.T) {
	c := provider.NaiveTokenCounter{}
	if _, err := c.Count(context.Background(), "anything at all \x00\xff"[:16]); err != nil {
		t.Errorf("Count: got error %v, want nil (NaiveTokenCounter never errors)", err)
	}
}
