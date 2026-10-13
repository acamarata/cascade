//go:build !windows

package claude

// Purpose: the callback frame shape rules, the OutputFilter pass over
//   tool_name and paths, and the row and byte bounds (T0 decisions F1/F2),
//   each proven to fail the whole run and stop the job, never to drop a
//   frame silently.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; tests
//   callback_frame.go. The shape table drives handle() on a bare channel;
//   the run-level cases go through a real job and its unix socket.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// substFilter redacts SUBSTME and errors on FILTERFAIL.
func substFilter(b []byte) ([]byte, error) {
	if bytes.Contains(b, []byte("FILTERFAIL")) {
		return nil, provider.ErrEgressSubstitutionFailed
	}
	return bytes.ReplaceAll(b, []byte("SUBSTME"), []byte("REDACTED")), nil
}

func bareChannel(t *testing.T) *callbackChannel {
	t.Helper()
	tok, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	return &callbackChannel{token: tok, class: provider.DataClassInternal, protocol: protocolRevision1,
		scan: newCanaryScanner(canaryForms("cascade-canary-test")), output: substFilter, norm: provider.NewEventNormalizer()}
}

func signed(c *callbackChannel, payload string) []byte {
	return []byte(hex.EncodeToString(c.token[:]) + " " + payload)
}

func TestCallbackFrameShapeRules(t *testing.T) {
	long := strings.Repeat("a", maxPathBytes+1)
	bad := map[string]string{
		"free-text tool_name":     `"tool_name":"model said: SUBSTME and the prompt text"`,
		"empty tool_name":         `"tool_name":""`,
		"129-byte tool_name":      `"tool_name":"` + strings.Repeat("t", 129) + `"`,
		"free-text result_hash":   `"tool_name":"x","result_hash":"the model output"`,
		"uppercase result_hash":   `"tool_name":"x","result_hash":"` + strings.Repeat("AB", 32) + `"`,
		"control char in a path":  `"tool_name":"x","paths":["a\nb"]`,
		"4097-byte path":          `"tool_name":"x","paths":["` + long + `"]`,
		"unclean path":            `"tool_name":"x","paths":["./a"]`,
		"dot-dot path":            `"tool_name":"x","paths":["../x"]`,
		"absolute path":           `"tool_name":"x","paths":["/etc/passwd"]`,
		"129-byte dedup_key":      `"tool_name":"x","dedup_key":"` + strings.Repeat("k", 129) + `"`,
		"unknown field (prompt)":  `"tool_name":"x","prompt":"free text"`,
		"unknown data_class":      `"tool_name":"x","data_class":"bogus"`,
		"negative duration":       `"tool_name":"x","duration_ms":-1`,
		"trailing second payload": `"tool_name":"x"}{"seq":2`,
	}
	for name, fields := range bad {
		c := bareChannel(t)
		fired := 0
		c.setFailHook(func() { fired++ })
		if ack := c.handle(signed(c, `{"seq":1,"kind":"status",`+fields+`}`)); ack != "refused payload" {
			t.Fatalf("%s: ack %q, want refused payload", name, ack)
		}
		if c.failure() != ErrCallbackFrameRejected || fired != 1 || len(c.accepted()) != 0 {
			t.Fatalf("%s: failure %v, hook fired %d, rows %d", name, c.failure(), fired, len(c.accepted()))
		}
		if ack := c.handle(signed(c, `{"seq":2,"kind":"status","tool_name":"x"}`)); ack != "refused failed" {
			t.Fatalf("%s: a frame after the failure got %q", name, ack)
		}
	}
	c := bareChannel(t)
	hash := strings.Repeat("0a", 32)
	ok := `{"seq":1,"kind":"status","tool_name":"mcp__srv.tool:v-1","result_hash":"` + hash + `","paths":["src/a.go","b"]}`
	if ack := c.handle(signed(c, ok)); ack != "ok" || c.failure() != nil {
		t.Fatalf("a valid frame got %q (%v)", ack, c.failure())
	}
	late := bareChannel(t)
	late.handle(signed(late, `{"seq":1,"kind":"status","tool_name":""}`))
	fired := false
	late.setFailHook(func() { fired = true })
	if !fired {
		t.Fatal("a failure latched before the hook was installed did not fire it")
	}
}

func TestCallbackRowBound(t *testing.T) {
	c := bareChannel(t)
	for i := 1; i <= maxCallbackRows; i++ {
		if ack := c.handle(signed(c, frameBody(i, ""))); ack != "ok" {
			t.Fatalf("frame %d: %q", i, ack)
		}
	}
	if ack := c.handle(signed(c, frameBody(maxCallbackRows+1, ""))); ack != "refused overflow" ||
		c.failure() != ErrFilteredBufferOverflow || len(c.accepted()) != maxCallbackRows {
		t.Fatalf("row %d: ack %q failure %v rows %d", maxCallbackRows+1, ack, c.failure(), len(c.accepted()))
	}
}

func frameBody(seq int, extra string) string {
	return `{"seq":` + itoa(seq) + `,"dedup_key":"k` + itoa(seq) + `","kind":"status","tool_name":"hook"` + extra + `}`
}

// collectUnreleased collects a job whose body waits for a release file that
// is never written: only the driver's own ladder can end it.
func collectUnreleased(t *testing.T, d *Driver, id provider.AgentJobID) (provider.CollectResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return d.Collect(ctx, id)
}

func TestClaudeCallbackFreeTextFailsRun(t *testing.T) {
	spec := func(f *fx) provider.AgentJobSpec { s := f.spec(); s.Filter = substFilter; return s }
	_, d, res, ci := openJobChannel(t, spec)
	n1 := frame(ci.Token, 1, "")
	n1 = strings.Replace(n1, `"tool_name":"hook"`, `"tool_name":"model said: SUBSTME and the prompt text"`, 1)
	if acks := sendFrames(t, ci.Socket, n1); len(acks) != 1 || acks[0] != "refused payload" {
		t.Fatalf("free-text tool_name acks %q", acks)
	}
	if cr, err := collectUnreleased(t, d, res.JobID); err != ErrCallbackFrameRejected || cr.Output != "" {
		t.Fatalf("Collect = %v (output %q): free text in a frame must fail and stop the run", err, cr.Output)
	}
	f, d, res, ci := openJobChannel(t, spec)
	sendFrames(t, ci.Socket, strings.Replace(frame(ci.Token, 1, `,"paths":["d/SUBSTME.txt"]`), "hook", "SUBSTME", 1))
	cr, err := finish(t, f, d, res.JobID)
	if err != nil || strings.Contains(cr.Output, "SUBSTME") || strings.Count(cr.Output, "REDACTED") != 2 {
		t.Fatalf("Collect = %v, output %q: tool_name and paths must persist only after the filter", err, cr.Output)
	}
	_, d, res, ci = openJobChannel(t, spec)
	sendFrames(t, ci.Socket, strings.Replace(frame(ci.Token, 1, ""), "hook", "FILTERFAIL", 1))
	if cr, err := collectUnreleased(t, d, res.JobID); err != provider.ErrEgressSubstitutionFailed || cr.Output != "" {
		t.Fatalf("Collect = %v (output %q): a filter error on a frame must fail and stop the run", err, cr.Output)
	}
}

func TestClaudeCallbackFloodFailsRun(t *testing.T) {
	_, d, res, ci := openJobChannel(t, func(f *fx) provider.AgentJobSpec { return f.spec() })
	list := make([]string, 15)
	for i := range list {
		list[i] = strings.Repeat("p", maxPathBytes)
	}
	paths, _ := json.Marshal(list)
	size, accepted := len(frame(ci.Token, 400, `,"paths":`+string(paths))), 0
	if size < 60<<10 || size > maxFrameBytes {
		t.Fatalf("flood frame is %d bytes, want 60 KiB", size)
	}
	for i := 1; i <= 400; i++ {
		acks := sendFrames(t, ci.Socket, frame(ci.Token, i, `,"paths":`+string(paths)))
		if len(acks) == 1 && acks[0] == "ok" {
			accepted++
			continue
		}
		if len(acks) != 1 || acks[0] != "refused overflow" {
			t.Fatalf("frame %d acks %q", i, acks)
		}
		break
	}
	if accepted == 400 || accepted*size > maxCallbackBytes {
		t.Fatalf("%d of 400 60-KiB frames accepted: the channel is not bounded at %d bytes", accepted, maxCallbackBytes)
	}
	if cr, err := collectUnreleased(t, d, res.JobID); err != ErrFilteredBufferOverflow || cr.Output != "" {
		t.Fatalf("Collect = %v (output %d bytes): the flood must fail and stop the run", err, len(cr.Output))
	}
}
