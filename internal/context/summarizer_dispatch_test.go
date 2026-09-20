package context

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestSummarizeTaskClassMatchesTheTaxonomy is the reason production code in
// internal/context can carry the literal "summarize" without importing
// internal/conductor (12-QUALITY-CONSTITUTION.md Art.10.2 keeps T1/T2 free
// of a conductor dependency): the literal is pinned HERE, in a test file
// where the conductor import is allowed, against the taxonomy's own
// constant. The literal cannot drift from §5.16 without this going red.
func TestSummarizeTaskClassMatchesTheTaxonomy(t *testing.T) {
	if summarizeTaskClass != string(conductor.TaskClassSummarize) {
		t.Fatalf("summarizeTaskClass = %q, want conductor.TaskClassSummarize = %q",
			summarizeTaskClass, conductor.TaskClassSummarize)
	}
}

// TestBuildModelRequestTaskClassAndShape asserts the task-class contract
// (06 §5.16: task_class = "summarize"), a non-empty TaskID, a single
// user-role input, and that the request neither declares a Policy nor
// widens Sensitivity (both must stay at their fail-closed zero values so the
// conductor's own privacy gate decides, never this caller).
func TestBuildModelRequestTaskClassAndShape(t *testing.T) {
	s := newTestSummarizer(t, &fakeExecutor{}, storetest.NewMemStore())
	req, err := s.buildModelRequest(context.Background(), "entity-1", GranularityThread, "", "hello world")
	if err != nil {
		t.Fatalf("buildModelRequest: unexpected error: %v", err)
	}
	if req.TaskClass != "summarize" {
		t.Errorf("TaskClass = %q, want %q", req.TaskClass, "summarize")
	}
	if req.TaskID == "" {
		t.Error("TaskID is empty, want a minted id")
	}
	if len(req.Inputs) != 1 || req.Inputs[0].Role != "user" {
		t.Fatalf("Inputs = %+v, want exactly one user-role message", req.Inputs)
	}
	if !strings.Contains(req.Inputs[0].Content, "hello world") {
		t.Errorf("Inputs[0].Content = %q, want it to contain the source content", req.Inputs[0].Content)
	}
	if req.Requirements.Reasoning != "low" {
		t.Errorf("Requirements.Reasoning = %q, want %q (06 §5.16 summarize row)", req.Requirements.Reasoning, "low")
	}
	if req.Policy != (provider.Policy{}) {
		t.Errorf("Policy = %+v, want the zero value: this caller must never widen the thread's inherited mode", req.Policy)
	}
	// SensitivityTier's zero value IS SensitivityRestricted by declaration
	// (pkg/provider/model.go, R-21.264), so an unset field is already the
	// fail-closed tier. Asserting the value, not merely "unset", is what
	// catches a future edit that widens it to internal or public.
	if req.Sensitivity != provider.SensitivityRestricted {
		t.Errorf("Sensitivity = %v, want the unset zero value SensitivityRestricted", req.Sensitivity)
	}
}

// TestBuildModelRequestTruncatesToWindow asserts source content longer than
// the granularity's code-default TOKEN window is truncated before dispatch,
// keeping the most recent (tail) content.
func TestBuildModelRequestTruncatesToWindow(t *testing.T) {
	s := newTestSummarizer(t, &fakeExecutor{}, storetest.NewMemStore())
	window := DefaultWindowTokens(GranularityTurnWindow)
	filler := strings.Repeat("a", window*4+400)
	req, err := s.buildModelRequest(context.Background(), "entity-1", GranularityTurnWindow, "", filler+"TAIL-MARKER")
	if err != nil {
		t.Fatalf("buildModelRequest: unexpected error: %v", err)
	}
	body := req.Inputs[0].Content
	if !strings.HasSuffix(body, "TAIL-MARKER") {
		t.Error("truncated content does not end with the tail marker; the window must keep the MOST RECENT content")
	}
	if strings.Contains(body, filler) {
		t.Error("truncated content still contains the full untruncated run of filler characters")
	}
	windowed := body[strings.Index(body, "CONTENT:\n")+len("CONTENT:\n"):]
	n, err := provider.NaiveTokenCounter{}.Count(context.Background(), windowed)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n > window {
		t.Errorf("windowed content measures %d tokens, want at most the %d-token window", n, window)
	}
}

// TestTruncateTailTokens is table-driven over short/exact/long inputs and a
// multi-byte input, and measures in TOKENS (the unit the composer's budget
// is in), never in runes.
func TestTruncateTailTokens(t *testing.T) {
	counter := provider.NaiveTokenCounter{}
	cases := []struct {
		name      string
		s         string
		maxTokens int
		want      string
	}{
		{"shorter than the bound", "abc", 10, "abc"},
		{"zero bound", "abcdefgh", 0, ""},
		{"negative bound", "abcdefgh", -1, ""},
		{"longer than the bound keeps the tail", "abcdefghijklmnop", 2, "fghijklmnop"},
		{"multi-byte runes truncate on rune boundaries", "日本語のテキストです", 1, "のテキストです"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := truncateTailTokens(context.Background(), counter, tc.s, tc.maxTokens)
			if err != nil {
				t.Fatalf("truncateTailTokens: %v", err)
			}
			if got != tc.want {
				t.Errorf("truncateTailTokens(%q, %d) = %q, want %q", tc.s, tc.maxTokens, got, tc.want)
			}
		})
	}
}

// TestTruncateHeadTokensKeepsHeadAndSentence asserts the substitute
// truncation keeps the summary's OPENING (a summary reads top-down) and ends
// on a sentence terminator when one is near enough to the cut.
func TestTruncateHeadTokensKeepsHeadAndSentence(t *testing.T) {
	counter := provider.NaiveTokenCounter{}
	text := strings.Repeat("Alpha beta gamma delta. ", 20)
	got, err := truncateHeadTokens(context.Background(), counter, text, 40)
	if err != nil {
		t.Fatalf("truncateHeadTokens: %v", err)
	}
	if !strings.HasPrefix(got, "Alpha beta gamma delta.") {
		t.Errorf("result = %q, want it to keep the HEAD of the summary", got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Errorf("result = %q, want it to end on a sentence terminator", got)
	}
	n, err := counter.Count(context.Background(), got)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n > 40 {
		t.Errorf("result measures %d tokens, want at most 40", n)
	}
	if whole, _ := counter.Count(context.Background(), text); whole <= 40 {
		t.Fatalf("fixture measures %d tokens, which does not exceed the 40-token bound this test needs", whole)
	}
}

// TestCutAtSentenceBoundary covers the three shapes: a terminator late
// enough to use, one too early to be worth the loss, and none at all.
func TestCutAtSentenceBoundary(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want string
	}{
		{"terminator in the second half", "one sentence. two sentence. three", "one sentence. two sentence."},
		{"terminator too early to use", "a. bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "a. bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{"no terminator at all", "no terminator here", "no terminator here"},
		{"newline counts as a terminator", "line one continues here\nand more", "line one continues here\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cutAtSentenceBoundary(tc.s); got != tc.want {
				t.Errorf("cutAtSentenceBoundary(%q) = %q, want %q", tc.s, got, tc.want)
			}
		})
	}
}

// TestDispatchExecuteSuccess asserts the model's Output is returned
// unchanged on success.
func TestDispatchExecuteSuccess(t *testing.T) {
	exec := &fakeExecutor{output: "model output"}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())
	req, err := s.buildModelRequest(context.Background(), "e", GranularityThread, "", "c")
	if err != nil {
		t.Fatalf("buildModelRequest: %v", err)
	}
	got, err := dispatchExecute(context.Background(), exec, req)
	if err != nil {
		t.Fatalf("dispatchExecute: unexpected error: %v", err)
	}
	if got != "model output" {
		t.Errorf("dispatchExecute = %q, want %q", got, "model output")
	}
}

// TestDispatchExecuteFailurePreservesKind asserts a failing executor's
// error Kind survives the wrap (errSummarizerDependency's contract).
func TestDispatchExecuteFailurePreservesKind(t *testing.T) {
	exec := &fakeExecutor{err: cascade.New(cascade.KindQuotaExhausted, "quota spent")}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())
	req, err := s.buildModelRequest(context.Background(), "e", GranularityThread, "", "c")
	if err != nil {
		t.Fatalf("buildModelRequest: %v", err)
	}
	_, err = dispatchExecute(context.Background(), exec, req)
	if err == nil {
		t.Fatal("dispatchExecute: want error")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindQuotaExhausted {
		t.Errorf("error kind = %v (ok=%v), want quota-exhausted (preserved from the executor's own error)", k, ok)
	}
}

// TestSummarizeReturnsSubstitute exercises the composer_types.go
// SummarizerGetter seam directly: a valid Slot returns a compressed
// substitute Slot carrying the same Kind and Label.
func TestSummarizeReturnsSubstitute(t *testing.T) {
	exec := &fakeExecutor{output: "short summary"}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())

	slot := Slot{Kind: SlotKindHistory, Label: "entity-x", Content: "a long block of conversation content"}
	got, err := s.Summarize(context.Background(), slot, 1000)
	if err != nil {
		t.Fatalf("Summarize: unexpected error: %v", err)
	}
	if got.Kind != SlotKindHistory || got.Label != "entity-x" {
		t.Errorf("Summarize = %+v, want Kind/Label preserved from the input slot", got)
	}
	if got.Content != "short summary" {
		t.Errorf("Summarize.Content = %q, want %q", got.Content, "short summary")
	}
}

// TestSummarizeEmptyLabelRefuses asserts an empty Label -- this adapter's
// entity id -- is refused rather than silently keyed under "".
func TestSummarizeEmptyLabelRefuses(t *testing.T) {
	s := newTestSummarizer(t, &fakeExecutor{output: "x"}, storetest.NewMemStore())
	_, err := s.Summarize(context.Background(), Slot{Content: "c"}, 100)
	if err == nil {
		t.Fatal("Summarize with empty Label: want error")
	}
}

// TestSummarizeTruncatesToRemainingBudget asserts the substitute never
// exceeds remainingBudget TOKENS as measured by the injected counter -- the
// same unit Compose's own budget accounting uses -- when the stored summary
// is larger than what is left.
func TestSummarizeTruncatesToRemainingBudget(t *testing.T) {
	const budget = 40
	counter := provider.NaiveTokenCounter{}
	exec := &fakeExecutor{output: strings.Repeat("Alpha beta gamma delta. ", 20)}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())

	got, err := s.Summarize(context.Background(), Slot{Label: "e", Content: "source"}, budget)
	if err != nil {
		t.Fatalf("Summarize: unexpected error: %v", err)
	}
	n, err := counter.Count(context.Background(), got.Content)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n > budget {
		t.Errorf("Summarize.Content measures %d tokens, want at most remainingBudget=%d", n, budget)
	}
	if !strings.HasPrefix(got.Content, "Alpha beta gamma delta.") {
		t.Errorf("Summarize.Content = %q, want the HEAD of the summary kept", got.Content)
	}
}

// TestSummarizeCachesIdenticalContent asserts two Summarize calls over an
// UNCHANGED slot dispatch exactly one model.execute call -- the
// determinism Compose's own SummarizerGetter doc comment requires for a
// fixed (slot, remainingBudget) pair.
func TestSummarizeCachesIdenticalContent(t *testing.T) {
	exec := &fakeExecutor{output: "cached summary"}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())
	slot := Slot{Label: "entity-y", Content: "unchanged content"}

	if _, err := s.Summarize(context.Background(), slot, 100); err != nil {
		t.Fatalf("first Summarize: %v", err)
	}
	if _, err := s.Summarize(context.Background(), slot, 100); err != nil {
		t.Fatalf("second Summarize: %v", err)
	}
	if got := exec.callCount(); got != 1 {
		t.Errorf("Execute called %d times for two calls over IDENTICAL content, want 1", got)
	}
}
