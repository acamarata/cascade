// Purpose: NaiveTokenCounter, the utf8-rune approximation R-14.97 ratifies
//   as a documented fallback TokenCounter: for tests, and for a caller that
//   has explicitly chosen it over a real tokenizer.
// Inputs: UTF-8 text.
// Outputs: an approximate token count; never an error.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2). The
//   estimate is utf8.RuneCountInString(text)/4 — counting Unicode code
//   points, never bytes, so a multi-byte script is not over-counted.
//   internal/context.Assemble never substitutes this counter for a nil one
//   (a nil TokenCounter is a typed error): a caller that wants the
//   approximation injects it explicitly.
// SPORT: pkg.provider.NaiveTokenCounter/ADDED (P1-E05-W2-S09-T1).

package provider

import (
	"context"
	"unicode/utf8"
)

// NaiveTokenCounter approximates token count as one token per four Unicode
// code points: utf8.RuneCountInString(text)/4, the rough ratio widely
// quoted for English prose under common subword tokenizers.
//
// This is NOT a production routing default. It exists for tests, for a
// caller with no real tokenizer available, and for any caller that has
// explicitly accepted its inaccuracy. A real tokenizer counts other scripts
// very differently: dense scripts such as Chinese or Arabic pack far fewer
// than four code points per model token, and this rune-based estimate does
// not correct for that. A caller doing real production routing needs a
// real, provider-supplied counter, not this one.
type NaiveTokenCounter struct{}

// Count returns utf8.RuneCountInString(text)/4, so a multi-byte character
// counts once toward the estimate, never once per byte. It never returns an
// error; ctx is accepted only to satisfy TokenCounter and is never read.
func (NaiveTokenCounter) Count(_ context.Context, text string) (int, error) {
	return utf8.RuneCountInString(text) / 4, nil
}
