package conversation

// Purpose: FuzzCursorDecode (06-FORGE-SPEC §5.7): the cursor decoder
//   accepts opaque, externally-supplied bytes (a caller could hand back a
//   corrupted, truncated, or hostile Cursor value), so this proves two
//   properties together -- decodeCursor never panics on ANY input, and
//   encode/decode round-trips to an identical payload for every value the
//   fuzzer explores. Seed corpus at
//   testdata/fuzz/FuzzCursorDecode/ (R-21.266: package-local, this
//   package only, never internal/testdata/fuzz/).
// SPORT: internal.conversation.pagination/ADDED (fuzz)
//   (P1-E20-W5-S44-T3).

import (
	"encoding/hex"
	"testing"
)

func FuzzCursorDecode(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("t."))
	f.Add([]byte("t.not-valid-base64!!"))
	f.Add([]byte("h.aGVsbG8"))
	f.Add([]byte("garbage-with-no-dot"))
	f.Add([]byte(`t.eyJ0IjoidGgxIiwicyI6M30`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Property 1: adversarial external bytes never panic the
		// decoder, whatever error (or lack of one) they produce. data is
		// used AS the raw opaque Cursor here -- arbitrary bytes,
		// including invalid UTF-8, which is exactly the "hostile Cursor
		// value" shape this target exists to cover.
		var probe turnCursorPayload
		_ = decodeCursor(Cursor(data), cursorKindTurn, &probe)

		// Property 2: round-trip identity. ThreadID is hex-encoded data
		// (always valid UTF-8/ASCII, matching every real ThreadID this
		// package ever produces -- NewTurnID's content-addressed hex
		// digest) rather than raw bytes: encoding/json.Marshal replaces
		// invalid UTF-8 in a string with U+FFFD, which would make the
		// round trip legitimately lossy for arbitrary binary content
		// that is not how this package's own ids are ever shaped.
		want := turnCursorPayload{ThreadID: hex.EncodeToString(data), Seq: int64(len(data))}
		enc, err := encodeCursor(cursorKindTurn, want)
		if err != nil {
			t.Fatalf("encodeCursor(%+v): %v", want, err)
		}
		var got turnCursorPayload
		if err := decodeCursor(enc, cursorKindTurn, &got); err != nil {
			t.Fatalf("decodeCursor(encodeCursor(%+v)) errored: %v", want, err)
		}
		if got != want {
			t.Fatalf("decode(encode(%+v)) = %+v, want identity", want, got)
		}
	})
}
