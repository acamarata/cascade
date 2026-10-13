//go:build !windows

package claude

// Purpose: the R-21.152 canary: a job that reads the seeded runtime-home
//   canary and emits it in any form, on any stream or as an artifact name,
//   fails closed; the negative control passes. Plus the canary forms, the
//   chunk-spanning scanner, and the bounded filteredWriter.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// readCanary is the shell fragment a leaking job uses to read the seeded
// canary from its runtime home.
const readCanary = `C=$(sed -n 's/^token=//p' "$HOME"/.cascade-credential-canary-*)` + "\n"

func TestClaudeCredentialCanaryFailsClosed(t *testing.T) {
	cases := map[string]string{
		"result text exact": `printf '{"type":"result","subtype":"success","is_error":false,"session_id":"s",` +
			`"total_cost_usd":0,"num_turns":1,"usage":{},"result":"%s"}\n' "$C"` + "\n",
		"stderr base64": `printf 'x%s' "$C" | base64 >&2` + "\n",
		"stdout hex":    `printf '%s' "$C" | od -An -tx1 | tr -d ' \n'; echo` + "\n",
		"artifact name": `touch "leak-$C.txt"` + "\n",
	}
	for name, leak := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.body = readCanary + `[ -n "$C" ] || exit 9` + "\n" + leak + f.body
			d := f.driver(f.config())
			res, cr, err := run(t, d, f.spec())
			if !errors.Is(err, ErrCredentialCanary) || !strings.Contains(err.Error(), "canary observed") {
				t.Fatalf("Collect = %v, want the canary to fail the run closed", err)
			}
			if cr.Output != "" || state(t, d, res.JobID) != provider.AgentRunFailed {
				t.Fatalf("output %q state %s", cr.Output, state(t, d, res.JobID))
			}
			if strings.Contains(err.Error(), "cascade-canary-") {
				t.Fatal("the failure message carries the canary itself")
			}
			matches, _ := filepath.Glob(filepath.Join(f.home, canaryFileName+"-*"))
			if len(matches) != 0 {
				t.Fatalf("the planted canary outlived the job: %v", matches)
			}
		})
	}
	t.Run("negative control", func(t *testing.T) {
		f := newFx(t)
		f.body = readCanary + `[ -n "$C" ] || exit 9` + "\n" + f.body
		if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
			t.Fatalf("a job that read but never emitted the canary failed: %v", err)
		}
	})
}

func TestCanaryFormsAndScanner(t *testing.T) {
	v := "cascade-canary-0123456789abcdef0123456789abcdef"
	forms := canaryForms(v)
	for k := 0; k < 3; k++ {
		prefix := strings.Repeat("z", k)
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			blob := []byte(enc.EncodeToString([]byte(prefix + v + "tail")))
			if !containsCanary(blob, forms) {
				t.Fatalf("offset %d: base64 form not found in %s", k, blob)
			}
		}
	}
	if !containsCanary([]byte(strings.ToUpper(hex.EncodeToString([]byte(v)))), forms) {
		t.Fatal("upper-case hex form not found")
	}
	if containsCanary([]byte("cascade-canary-0123 unrelated"), forms) {
		t.Fatal("a prefix alone matched")
	}
	s := newCanaryScanner(forms)
	s.Scan([]byte("noise " + v[:20]))
	if s.Hit() {
		t.Fatal("half a canary hit")
	}
	s.Scan([]byte(v[20:] + " more"))
	if !s.Hit() {
		t.Fatal("a canary split across two chunks was missed")
	}
}

func TestFilteredWriterBounds(t *testing.T) {
	w := &filteredWriter{f: func(b []byte) ([]byte, error) { return bytes.ToUpper(b), nil }}
	if err := w.Write([]byte("abc")); err != nil || string(w.Bytes()) != "ABC" {
		t.Fatalf("filtered bytes %q, %v", w.Bytes(), err)
	}
	if err := w.Write(make([]byte, maxFilteredBufferBytes)); err != ErrFilteredBufferOverflow || !w.Overflowed() {
		t.Fatalf("overflow: %v", err)
	}
	if err := w.Write([]byte("x")); err != nil || len(w.Bytes()) != 3 {
		t.Fatal("bytes were buffered past the bound")
	}
	bad := &filteredWriter{f: func([]byte) ([]byte, error) { return nil, errors.New("no") }}
	if err := bad.Write([]byte("secret")); err != errFilterFailed || len(bad.Bytes()) != 0 {
		t.Fatalf("filter error: %v, buffered %q", err, bad.Bytes())
	}
	bad.f = func(b []byte) ([]byte, error) { return b, nil }
	if _ = bad.Write([]byte("later")); len(bad.Bytes()) != 0 {
		t.Fatal("a chunk after a filter error was buffered")
	}
}
