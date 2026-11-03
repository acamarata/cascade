package codex

import "testing"

// FuzzCodexAppServerFrame: the decoder never panics, fails only with
// ErrMalformedFrame and classifies every frame it accepts.
func FuzzCodexAppServerFrame(f *testing.F) {
	f.Add([]byte(`{"id":1,"result":{}}`))
	f.Add([]byte(`{"method":"turn/completed","params":{}}`))
	f.Fuzz(func(t *testing.T, line []byte) {
		fr, err := decodeFrame(line)
		if err != nil {
			mustSentinel(t, err, ErrMalformedFrame)
			return
		}
		switch fr.Kind {
		case frameRequest, frameNotification:
			if fr.Method == "" {
				t.Fatal("a method frame without a method")
			}
		case frameResponse:
			if len(fr.ID) == 0 {
				t.Fatal("a response without an id")
			}
		default:
			t.Fatalf("unclassified frame kind %d", fr.Kind)
		}
	})
}
