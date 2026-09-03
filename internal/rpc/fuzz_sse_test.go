package rpc

// Purpose: FuzzParseResumeToken — the fuzz target this ticket's contract
//   requires (06-FORGE-SPEC §5 rule 7: parseResumeToken decodes untrusted
//   client input, the Last-Event-ID header). Its property is: it never
//   panics and never returns an error — every input resolves to either a
//   valid cursor or "open at tail" (R-14.13's SHOULD semantics).
// Constraints: seed corpus at
//   internal/testdata/fuzz/FuzzParseResumeToken/ (never repo root), with a
//   provenance README this test asserts exists.

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// sseFuzzSeedDir mirrors fuzz_test.go's fuzzSeedDir convention for this
// ticket's own corpus.
const sseFuzzSeedDir = "../testdata/fuzz/FuzzParseResumeToken"

// TestFuzzParseResumeTokenSeedProvenanceExists asserts the corpus
// provenance README this ticket's contract requires actually exists.
func TestFuzzParseResumeTokenSeedProvenanceExists(t *testing.T) {
	path := filepath.Join(sseFuzzSeedDir, "README.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("provenance README missing at %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("%s is a directory, want a file", path)
	}
}

// loadSSEFuzzSeedFiles reads every *.txt file in sseFuzzSeedDir and
// returns its raw contents as seed strings, mirroring fuzz_test.go's
// loadFuzzSeedFiles for the JSON corpus (a resume token is opaque text,
// not JSON, hence the different extension).
func loadSSEFuzzSeedFiles(f *testing.F) []string {
	f.Helper()
	entries, err := os.ReadDir(sseFuzzSeedDir)
	if err != nil {
		f.Fatalf("reading fuzz seed dir %s: %v", sseFuzzSeedDir, err)
	}
	var seeds []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(sseFuzzSeedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed file %s: %v", e.Name(), readErr)
		}
		seeds = append(seeds, string(data))
	}
	if len(seeds) == 0 {
		f.Fatalf("no *.txt seed files found in %s", sseFuzzSeedDir)
	}
	return seeds
}

// FuzzParseResumeToken proves parseResumeToken never panics and never
// returns an error for arbitrary input — only ever (seq, true) for a
// well-formed 8-byte base64url cursor, or (0, false) otherwise.
func FuzzParseResumeToken(f *testing.F) {
	for _, s := range loadSSEFuzzSeedFiles(f) {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseResumeToken panicked on input %q: %v", in, r)
			}
		}()
		seq, ok := parseResumeToken(in)
		if !ok && seq != 0 {
			t.Fatalf("parseResumeToken(%q) = (%d, false), want seq 0 when not ok", in, seq)
		}
		if ok {
			// A recognized token must round-trip through formatResumeToken
			// to the exact same wire string, proving the decode was of a
			// real, well-formed 8-byte cursor and not a coincidental
			// same-length garbage decode.
			want := strings.TrimSpace(in)
			if got := formatResumeToken(seq); got != want {
				t.Fatalf("parseResumeToken(%q) = (%d, true) but formatResumeToken(%d) = %q, want round-trip to %q", in, seq, seq, got, want)
			}
		}
	})
}

// failingStore wraps a real MemStore, injecting one Store-level failure so
// a real Bus (never a fake) still surfaces a real error path: scanFail
// covers SSEHandler's tail-lookup (Bus.Replay) error branch; putCursorFail
// covers stream's sub.Errs branch (deliverLoop's commitCursor fails right
// after a real delivery already succeeded).
type failingStore struct {
	*storetest.MemStore
	scanFail      bool
	putCursorFail bool
}

func (s *failingStore) Scan(ctx context.Context, ns, prefix string) (provider.Iterator, error) {
	if s.scanFail {
		return nil, cascade.New(cascade.KindUnavailable, "injected scan failure")
	}
	return s.MemStore.Scan(ctx, ns, prefix)
}

func (s *failingStore) Put(ctx context.Context, ns, key string, v []byte) error {
	if s.putCursorFail && strings.HasPrefix(key, "cursor:") {
		return cascade.New(cascade.KindUnavailable, "injected cursor commit failure")
	}
	return s.MemStore.Put(ctx, ns, key, v)
}

func TestSSEHandler_WrongPathOrMethod_404(t *testing.T) {
	bus, clock := newTestBus()
	t.Cleanup(func() { _ = bus.Close() })
	h := NewSSEHandler(bus, "ns", knownAB, clock)

	assert404 := func(method, path string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != 404 {
			t.Errorf("%s %s: status = %d, want 404", method, path, rec.Code)
		}
	}
	assert404("GET", "/not-events")
	assert404("POST", EventsPath)
}

func TestSSEHandler_TailLookupError_500(t *testing.T) {
	store := &failingStore{MemStore: storetest.NewMemStore(), scanFail: true}
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(store, clock)
	t.Cleanup(func() { _ = bus.Close() })
	h := NewSSEHandler(bus, "ns", knownAB, clock)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", EventsPath, nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500 when the tail lookup fails", rec.Code)
	}
}

func TestSSEHandler_DeliveryError_ClosesStream(t *testing.T) {
	store := &failingStore{MemStore: storetest.NewMemStore(), putCursorFail: true}
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(store, clock)
	t.Cleanup(func() { _ = bus.Close() })
	h := NewSSEHandler(bus, "ns", knownAB, clock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, done := runSSE(ctx, h, "", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })
	mustPublish(t, bus, "ns", kindA, "1")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not exit after a fatal delivery error")
	}
}

// TestHandler_NewHandlerWithSSE_MountsGETEvents proves handler.go's mount
// point actually dispatches a real GET /events request to the SSEHandler
// it was built with, alongside the POST /rpc route T3 already tests.
func TestHandler_NewHandlerWithSSE_MountsGETEvents(t *testing.T) {
	bus, clock := newTestBus()
	t.Cleanup(func() { _ = bus.Close() })
	sse := NewSSEHandler(bus, "ns", knownAB, clock)
	h := NewHandlerWithSSE(NewRegistry(), sse)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newSyncRecorder()
	req := httptest.NewRequest("GET", EventsPath, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, req); close(done) }()
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Handler did not mount GET /events on SSEHandler: Content-Type = %q", got)
	}
	cancel()
	<-done
}
