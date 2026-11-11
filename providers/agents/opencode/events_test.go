package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/conformance"
)

func TestOpenCodeConformanceReplay(t *testing.T) {
	k := newKit(t)
	_ = k.driver()
	s := &conformance.Suite{Env: k.env, Worktree: k.root, Filter: identity, New: func() (provider.AgentProvider, error) {
		d, err := New(k.cfg)
		if err == nil {
			t.Cleanup(func() {
				for id := range d.jobs {
					_ = d.Cancel(context.Background(), id)
				}
			})
		}
		return d, err
	}}
	v := reflect.ValueOf(s)
	count := 0
	for i := 0; i < v.NumMethod(); i++ {
		if fn, ok := v.Method(i).Interface().(func(*testing.T)); ok && strings.HasPrefix(v.Type().Method(i).Name, "Test") {
			count++
			t.Run(v.Type().Method(i).Name, fn)
		}
	}
	if count != 20 {
		t.Fatalf("ran %d suite cases", count)
	}
}

func TestOpenCodeEventMappingGoldens(t *testing.T) {
	b, err := os.ReadFile("testdata/run.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	g, err := os.ReadFile("testdata/mapping.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var gold map[string]provider.DriverEventKind
	if err := json.Unmarshal(g, &gold); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, line := range bytes.Split(bytes.TrimSpace(b), []byte{'\n'}) {
		e, session, err := decodeEvent(line, uint64(i+1), provider.DataClassInternal)
		if err != nil {
			t.Fatal(err)
		}
		seen[e.Native.Type] = true
		if e.Kind != gold[e.Native.Type] || !bytes.Equal(e.Native.Raw, line) || session != "ses_fixture" {
			t.Fatalf("mapping: %+v", e)
		}
		checkEventGolden(t, e)
	}
	if len(seen) != len(gold) || len(seen) != len(eventKinds) {
		t.Fatal("mapping inventory differs from fixture")
	}
}

func checkEventGolden(t *testing.T, e provider.DriverEvent) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + e.Native.Type + ".golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want provider.DriverEvent
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	e.Native = nil
	if !reflect.DeepEqual(e, want) {
		t.Fatalf("event=%+v want=%+v", e, want)
	}
}

func TestOpenCodeUnknownKindTyped(t *testing.T) {
	_, _, err := decodeEvent([]byte(`{"type":"future"}`), 1, provider.DataClassInternal)
	wantError(t, err, ErrUnknownEventKind)
}

func TestOpenCodeNativePartsPassThrough(t *testing.T) {
	k := newKit(t)
	r, c, err := k.run(k.spec())
	if err != nil || c.Output != "ready" {
		t.Fatalf("%+v %v", c, err)
	}
	e, _ := k.d.Events(r.JobID)
	if len(e) != 3 {
		t.Fatalf("events=%d", len(e))
	}
	lines := strings.Split(strings.TrimSpace(k.opts.Lines), "\n")
	for i, v := range e {
		if string(v.Native.Raw) != lines[i] || v.Protocol != ProtocolRevision {
			t.Fatal("native mismatch")
		}
	}
	e[0].Native.Raw[0] = 'x'
	again, _ := k.d.Events(r.JobID)
	if again[0].Native.Raw[0] == 'x' {
		t.Fatal("aliased native bytes")
	}
}

func TestOpenCodeEventsDedupAndOrder(t *testing.T) {
	j := &job{tier: provider.DataClassInternal, norm: provider.NewEventNormalizer()}
	raw := []byte(`{"type":"text","part":{"id":"one","text":"ready"}}`)
	if err := j.record(raw); err != nil {
		t.Fatal(err)
	}
	if err := j.record(raw); err != nil {
		t.Fatal(err)
	}
	if len(j.events) != 1 {
		t.Fatal("duplicate emitted")
	}
	_, _, err := j.norm.Normalize(provider.DriverEvent{Kind: provider.DriverEventStdout, Seq: 1, DedupKey: "new"})
	wantError(t, err, provider.ErrOutOfOrderEvent)
}

func TestOpenCodeArgvGolden(t *testing.T) {
	d, _ := New(Config{Binary: "/fixture/opencode"})
	s := provider.AgentJobSpec{WorktreeRoot: "/fixture/worktree", Model: "zhipu/glm", Effort: "high", Prompt: "-literal prompt"}
	b, err := os.ReadFile("testdata/argv.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.argv(s), want) {
		t.Fatalf("argv=%q", d.argv(s))
	}
}

func FuzzOpenCodeEvent(f *testing.F) {
	b, err := os.ReadFile("testdata/run.jsonl")
	if err != nil {
		f.Fatal(err)
	}
	for _, l := range bytes.Split(bytes.TrimSpace(b), []byte{'\n'}) {
		f.Add(l)
	}
	f.Add([]byte(`{"type":"future"}`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, b []byte) {
		e, _, err := decodeEvent(b, 1, provider.DataClassRestricted)
		if err == nil && (e.Native == nil || !bytes.Equal(e.Native.Raw, b)) {
			t.Fatal("native loss")
		}
	})
}
