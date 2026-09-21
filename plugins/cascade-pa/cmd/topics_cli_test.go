// Purpose (this file): behavioral coverage for `cascade chat --topics`,
//
//	`--threads`, and `--thread <slug>` (U/S-46.T4): round-trip list
//	output, pagination, valid/unknown-slug open, topic-engine-not-ready
//	propagation, the unconfigured-client Art.1 floor, and — package
//	cmd_test (external), so it can import plugins/cascade-pa without a
//	real import cycle — the Art.2 real-counterpart requirement: the new
//	flags dispatch through plugin.Builtins()'s REAL builtin registry
//	entry for "cascade-pa", not a self-authored mock BuiltinHandlers.
//
// Inputs: fakeTopicsClient doubles injected via cmd.SetTopicsThreadsClient.
// Outputs: n/a (test file).
// Constraints: no TTY, no real daemon, no network (internal/build's
//
//	TestNoNetworkUnitTest gate).
//
// SPORT: plugin.cascade-pa:topics-cli (ADD) — P1-E21-W5-S46-T4.
package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	cmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"

	// Blank import to trigger cascadepa's init() -> plugin.RegisterBuiltin,
	// so plugin.Builtins() has a "cascade-pa" entry (Art.2 real registry).
	_ "github.com/acamarata/cascade/plugins/cascade-pa"
)

// fakeTopicsClient is a cmd.TopicsThreadsClient test double. A nil *Fn
// field panics if called, surfacing an unintended-path test bug
// immediately, matching cmd's own fakeClient convention.
type fakeTopicsClient struct {
	ListTopicsFn  func(ctx context.Context) ([]cmd.TopicSummary, error)
	ListThreadsFn func(ctx context.Context, req cmd.ThreadsListRequest) (cmd.ThreadsListResult, error)
	OpenThreadFn  func(ctx context.Context, slug string) (cmd.ThreadSummary, error)
}

func (f fakeTopicsClient) ListTopics(ctx context.Context) ([]cmd.TopicSummary, error) {
	return f.ListTopicsFn(ctx)
}

func (f fakeTopicsClient) ListThreads(ctx context.Context, req cmd.ThreadsListRequest) (cmd.ThreadsListResult, error) {
	return f.ListThreadsFn(ctx, req)
}

func (f fakeTopicsClient) OpenThread(ctx context.Context, slug string) (cmd.ThreadSummary, error) {
	return f.OpenThreadFn(ctx, slug)
}

// execChat builds a fresh chat command, runs it with args against
// buffered out/err streams (no TTY), and returns stdout plus the error.
func execChat(t *testing.T, args []string) (stdout string, err error) {
	t.Helper()
	c := cmd.NewChatCommand()
	var out, errOut, in bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)
	// A bytes.Buffer is never a *os.File, so isRealTTY(cc.InOrStdin())
	// reads false deterministically -- without this, cobra falls back to
	// the REAL os.Stdin, whose TTY-ness depends on how the test process
	// itself was launched (D4's non-interactive-thread-open tests below
	// need this to be reliable in every environment, not just CI).
	c.SetIn(&in)
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetContext(context.Background())
	c.SetArgs(args)
	err = c.Execute()
	return out.String(), err
}

func TestChatTopicsListRoundTrip(t *testing.T) {
	cmd.SetTopicsThreadsClient(fakeTopicsClient{
		ListTopicsFn: func(context.Context) ([]cmd.TopicSummary, error) {
			return []cmd.TopicSummary{{Label: "code", ThreadCount: 3}, {Label: "general", ThreadCount: 1}}, nil
		},
	})
	t.Cleanup(func() { cmd.SetTopicsThreadsClient(nil) })

	out, err := execChat(t, []string{"--topics", "--json"})
	if err != nil {
		t.Fatalf("execChat: %v", err)
	}
	var got struct {
		Topics []cmd.TopicSummary `json:"topics"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", out, err)
	}
	if len(got.Topics) != 2 || got.Topics[0].Label != "code" || got.Topics[0].ThreadCount != 3 {
		t.Fatalf("topics = %+v, want [{code 3} {general 1}]", got.Topics)
	}
}

func TestChatThreadsListPagination(t *testing.T) {
	var gotReq cmd.ThreadsListRequest
	cmd.SetTopicsThreadsClient(fakeTopicsClient{
		ListThreadsFn: func(_ context.Context, req cmd.ThreadsListRequest) (cmd.ThreadsListResult, error) {
			gotReq = req
			return cmd.ThreadsListResult{
				Threads:    []cmd.ThreadSummary{{ID: "th1", Slug: "s1", Title: "Thread One", MessageCount: 4}},
				Page:       req.Page,
				PageSize:   req.PageSize,
				TotalCount: 11,
			}, nil
		},
	})
	t.Cleanup(func() { cmd.SetTopicsThreadsClient(nil) })

	out, err := execChat(t, []string{"--threads", "--json", "--page", "2", "--page-size", "5"})
	if err != nil {
		t.Fatalf("execChat: %v", err)
	}
	if gotReq.Page != 2 || gotReq.PageSize != 5 {
		t.Fatalf("request = %+v, want page=2 page_size=5", gotReq)
	}
	var got cmd.ThreadsListResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", out, err)
	}
	if len(got.Threads) != 1 || got.Threads[0].Slug != "s1" || got.TotalCount != 11 {
		t.Fatalf("result = %+v", got)
	}
}

func TestChatThreadOpenValidSlug(t *testing.T) {
	cmd.SetTopicsThreadsClient(fakeTopicsClient{
		OpenThreadFn: func(_ context.Context, slug string) (cmd.ThreadSummary, error) {
			if slug != "my-thread" {
				t.Fatalf("OpenThread: slug = %q, want my-thread", slug)
			}
			return cmd.ThreadSummary{ID: "th9", Slug: slug, Title: "My Thread", MessageCount: 7}, nil
		},
	})
	t.Cleanup(func() { cmd.SetTopicsThreadsClient(nil) })

	out, err := execChat(t, []string{"--thread", "my-thread", "--json"})
	if err != nil {
		t.Fatalf("execChat: %v", err)
	}
	var got cmd.ThreadSummary
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", out, err)
	}
	if got.Slug != "my-thread" || got.MessageCount != 7 {
		t.Fatalf("summary = %+v", got)
	}
}

// TestChatThreadOpenUnknownSlugExitsNonZero: an unknown slug's typed
// KindNotFound error propagates unchanged — non-zero exit, actionable
// stderr text (the slug itself, quoted) — never a silent success.
func TestChatThreadOpenUnknownSlugExitsNonZero(t *testing.T) {
	wantErr := cascade.New(cascade.KindNotFound, `cascade chat: thread "ghost" not found`)
	cmd.SetTopicsThreadsClient(fakeTopicsClient{
		OpenThreadFn: func(context.Context, string) (cmd.ThreadSummary, error) {
			return cmd.ThreadSummary{}, wantErr
		},
	})
	t.Cleanup(func() { cmd.SetTopicsThreadsClient(nil) })

	_, err := execChat(t, []string{"--thread", "ghost", "--json"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("execChat: err = %v, want %v", err, wantErr)
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err kind: got %v, want KindNotFound", err)
	}
}

// TestChatTopicsEngineNotReadyPropagates: a topic-engine-not-ready error
// from the client surfaces as a non-zero exit with the real error, never
// downgraded to an empty topics list.
func TestChatTopicsEngineNotReadyPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "cascade chat --topics: topic engine not ready (observe window active)")
	cmd.SetTopicsThreadsClient(fakeTopicsClient{
		ListTopicsFn: func(context.Context) ([]cmd.TopicSummary, error) {
			return nil, wantErr
		},
	})
	t.Cleanup(func() { cmd.SetTopicsThreadsClient(nil) })

	out, err := execChat(t, []string{"--topics", "--json"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("execChat: err = %v, want %v", err, wantErr)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty (no silent empty-list success)", out)
	}
}

// TestChatTopicsUnconfiguredClientReturnsActionableError: Art.1 floor —
// with no TopicsThreadsClient wired, --topics fails with a typed,
// actionable KindUnavailable error, never an empty list.
func TestChatTopicsUnconfiguredClientReturnsActionableError(t *testing.T) {
	cmd.SetTopicsThreadsClient(nil)
	_, err := execChat(t, []string{"--topics"})
	if err == nil {
		t.Fatal("execChat: want error when no topics client is wired, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err kind: got %v, want KindUnavailable", err)
	}
}

// TestChatThreadOpenNoJSONNonInteractivePrintsText: `--thread <slug>` with
// no --json and no real TTY (execChat's harness always uses a
// bytes.Buffer for stdin, never a TTY) prints the same summary as plain
// text and exits 0, rather than attempting to open the TUI (06-FORGE-SPEC
// §5.8 non-interactive parity, D4).
func TestChatThreadOpenNoJSONNonInteractivePrintsText(t *testing.T) {
	cmd.SetTopicsThreadsClient(fakeTopicsClient{
		OpenThreadFn: func(_ context.Context, slug string) (cmd.ThreadSummary, error) {
			return cmd.ThreadSummary{ID: "th9", Slug: slug, Title: "My Thread", MessageCount: 3}, nil
		},
	})
	t.Cleanup(func() { cmd.SetTopicsThreadsClient(nil) })

	out, err := execChat(t, []string{"--thread", "my-thread"})
	if err != nil {
		t.Fatalf("execChat: %v", err)
	}
	if !strings.Contains(out, "th9") || !strings.Contains(out, "my-thread") || !strings.Contains(out, "3") {
		t.Fatalf("stdout = %q, want the thread summary as text (no TUI attempted)", out)
	}
}

// TestChatThreadOpenNoJSONCascadeNoInputPrintsText: the same as above, but
// driven by CASCADE_NO_INPUT=1 instead of TTY absence — both signals gate
// the same way (D4).
func TestChatThreadOpenNoJSONCascadeNoInputPrintsText(t *testing.T) {
	cmd.SetTopicsThreadsClient(fakeTopicsClient{
		OpenThreadFn: func(_ context.Context, slug string) (cmd.ThreadSummary, error) {
			return cmd.ThreadSummary{ID: "th9", Slug: slug, Title: "My Thread", MessageCount: 3}, nil
		},
	})
	t.Cleanup(func() { cmd.SetTopicsThreadsClient(nil) })

	t.Setenv("CASCADE_NO_INPUT", "1")
	out, err := execChat(t, []string{"--thread", "my-thread"})
	if err != nil {
		t.Fatalf("execChat: %v", err)
	}
	if !strings.Contains(out, "th9") {
		t.Fatalf("stdout = %q, want the thread summary as text", out)
	}
}

// TestChatTopicsThreadsMountViaRealBuiltinRegistry is the ticket's Art.2
// acceptance criterion: --topics dispatches through plugin.Builtins()'s
// REAL "cascade-pa" registration (populated by the blank import above's
// init() -> plugin.RegisterBuiltin call), not a self-authored mock
// BuiltinHandlers.
func TestChatTopicsThreadsMountViaRealBuiltinRegistry(t *testing.T) {
	cmd.SetTopicsThreadsClient(fakeTopicsClient{
		ListTopicsFn: func(context.Context) ([]cmd.TopicSummary, error) {
			return []cmd.TopicSummary{{Label: "general", ThreadCount: 2}}, nil
		},
	})
	t.Cleanup(func() { cmd.SetTopicsThreadsClient(nil) })

	var found bool
	for _, reg := range plugin.Builtins() {
		if reg.Manifest.ID != "cascade-pa" {
			continue
		}
		found = true
		if err := reg.Handlers.RunCommand(context.Background(), "chat", []string{"--topics", "--json"}); err != nil {
			t.Fatalf("RunCommand(chat, --topics) via real registry: %v", err)
		}
	}
	if !found {
		t.Fatal(`plugin.Builtins() has no "cascade-pa" entry -- blank import missing or init() not registering`)
	}
}
