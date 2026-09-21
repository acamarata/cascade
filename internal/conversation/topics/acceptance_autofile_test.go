// Package topics (acceptance_autofile_test.go): Purpose: the P1-E21-W5-
// S46-T5 Story 1 acceptance proof for topic auto-filing. Drives the REAL
// shipped units end to end - NewSegmenter, NewAutoThreader,
// NewConversationThreadStore over a real sqlite conversation.Store,
// NewTaxonomyConfig, NewExemplarStore, ObserveLogger - with fakes ONLY at
// the true provider seam (provider.Embedder, provider.ModelExecutor),
// exactly the pattern P1-E21-W5-S46-T4's own composition-seam tests use
// (cmd/cascade/chat_topics_engine_test.go), reused here in-package since
// this ticket's files_scope has no cmd/cascade door.
//
// Inputs: the real S-45.T1 owner corpus path (testdata/corpus/, currently
// undelivered - see testdata/acceptance/README.md) and the openly-
// synthetic four-topic fixture segmenter_harness_test.go already
// established (syntheticCorpus, keywordClassifier, onehotEmbedder,
// scoreCorpus), reused here rather than re-invented.
//
// Outputs: none (t.Fatal/t.Skip only).
//
// Constraints: Art.2 - the RealOwnerCorpus subtest asserts against the
// real corpus path and skips honestly while it is empty (12-QUALITY-
// CONSTITUTION.md Art.2, per corpus/README.md's own "not yet delivered"
// status); it never substitutes a self-authored dialect for that specific
// claim. The routing-and-floors subtests below prove the MECHANISM against
// the pre-existing, already-reviewed synthetic fixture, labelled as such,
// which is a distinct claim from Art.2's real-corpus requirement. No
// network; no bare time.Now (fixedClock only). Every sqlite handle closes
// before TempDir cleanup (t.Cleanup).
// SPORT: internal/conversation/topics acceptance (ADD) (P1-E21-W5-S46-T5).
package topics

import (
	"context"
	"testing"
)

// TestAcceptanceAutoFile is the checks-list entry point (LANE-RULES): every
// Story 1 claim is one subtest, so `-run TestAcceptanceAutoFile -v` reports
// one === RUN line per claim.
func TestAcceptanceAutoFile(t *testing.T) {
	t.Run("RealOwnerCorpus", acceptanceRealOwnerCorpus)
	t.Run("AccuracyFloors", acceptanceAccuracyFloors)
	t.Run("PipelineFilesExactlyFourDiscreteThreads", acceptancePipelineFilesFourThreads)
	t.Run("ObserveLogDefaultIsPresentForANewThread", acceptanceObserveLogDefaultPresent)
}

// acceptanceRealOwnerCorpus is Art.2's binding claim: at least one
// acceptance test asserts against the real S-45.T1 owner corpus, not a
// self-authored dialect. It loads the real path with the real loader. The
// owner prerequisite (06-FORGE-SPEC §7) was never delivered as of this
// ticket (internal/conversation/topics/testdata/corpus/README.md, and
// P1-E21-W5-S45-T1's/T2's journals HONEST GAPS/"Could not honestly
// complete") - `find internal/conversation/topics/testdata/corpus -name
// '*.json'` returns zero files in this tree - so this test SKIPS rather
// than fabricating a pass: a green result here would claim accuracy floors
// were measured against real data when they were not.
// acceptanceCorpusDir is the real S-45.T1 owner-corpus path, defined here
// rather than reusing segmenter_corpus_test.go's identical `corpusDir`
// because that constant lives behind the `topics_corpus` build tag
// (LANE-RULES: no build tags on in-process tests) and would be undefined
// in this file's untagged build.
const acceptanceCorpusDir = "testdata/corpus"

func acceptanceRealOwnerCorpus(t *testing.T) {
	corpus, err := LoadCorpus(acceptanceCorpusDir)
	if err != nil {
		t.Skipf("UNEXECUTED OWNER PREREQUISITE (06-FORGE-SPEC §7, P1-E21-W5-S45-T1): %s is unreadable, "+
			"so Art.2's real-corpus claim has never run against real data: %v", acceptanceCorpusDir, err)
	}
	if len(corpus) == 0 {
		t.Skipf("UNEXECUTED OWNER PREREQUISITE (06-FORGE-SPEC §7): %s holds no *.json records. The owner "+
			"corpus was never delivered (see testdata/corpus/README.md); this acceptance ticket cannot "+
			"honestly claim the exactly-4-threads/accuracy-floor criteria against real data, so this "+
			"subtest reports that gap instead of a fabricated pass. The mechanism itself is proven against "+
			"the openly-synthetic fixture by AccuracyFloors and PipelineFilesExactlyFourDiscreteThreads.",
			acceptanceCorpusDir)
	}
	// A real, non-empty corpus would land here: score it exactly as
	// AccuracyFloors below scores the synthetic one.
	result := scoreCorpus(t, corpus, newSyntheticSegmenter(t))
	if err := AssertFloors(result); err != nil {
		t.Fatalf("real owner corpus missed the accuracy floors: %v (result=%+v)", err, result)
	}
}

// acceptanceAccuracyFloors is the ticket's "Accuracy floors apply" claim,
// scored on the pre-existing, already-reviewed synthetic fixture
// (segmenter_harness_test.go's syntheticCorpus/scoreCorpus/
// newSyntheticSegmenter, reused unmodified rather than re-implemented) -
// openly labelled synthetic, a mechanism proof, not Art.2's real-corpus
// claim (that is acceptanceRealOwnerCorpus above).
func acceptanceAccuracyFloors(t *testing.T) {
	corpus := syntheticCorpus()
	result := scoreCorpus(t, corpus, newSyntheticSegmenter(t))
	if err := AssertFloors(result); err != nil {
		t.Fatalf("real Segmenter missed the floors on the labelled synthetic fixture: %v (result=%+v)",
			err, result)
	}
	if result.BoundaryF1 < BoundaryF1Floor || result.AssignmentAccuracy < AssignmentAccuracyFloor {
		t.Fatalf("result = %+v, below the 06-FORGE-SPEC §5.12 floors (F1>=%.2f, accuracy>=%.2f)",
			result, BoundaryF1Floor, AssignmentAccuracyFloor)
	}
}

// acceptancePipelineFilesFourThreads drives Segment -> AutoThreader.Route
// -> a real sqlite-backed ThreadStore over syntheticCorpus's three
// records, whose union of topics is exactly the closed four-member
// vocabulary {alpha, beta, gamma, delta} (segmenter_harness_test.go). Each
// record is routed as its own append (a separate call to Route, the same
// shape a real conversation delivers turns in); ThreadStore.CreateOrSelect
// is keyed by TopicType alone, so the same topic across different records
// selects the same thread - proving the pipeline converges on exactly one
// thread per topic, four total, never one thread per record (twelve) or
// one per turn.
//
// FALLBACK = "general" (P1-E21-W5-S46-T5 D5 fix, 2026-09-21): every Route
// call's implicit opening segment is now classified by the SAME cheap-lane
// classifier the rest of the window uses (auto_thread.go's SEGMENT-ZERO IS
// CLASSIFIED HERE), so each of the three records' openers resolves through
// TaxonomyConfig on its own real content - synthetic-1/-2/-3 open on
// "alpha"/"beta"/"gamma" text respectively (syntheticCorpus,
// segmenter_harness_test.go) - and files into that topic's thread, one of
// the four the fixture already produces from its later boundaries. Nothing
// in this fixture's real content is topic-less, so the "general" fallback
// is never actually hit by a successful run; it stays configured (rather
// than an invented word like the prior, rejected "alpha" fallback) because
// it is a genuine, honest name for "no recognized topic", matching
// production's own intent (cascadepa's default label set, taxonomy.go's
// header). Before this fix, ALL THREE records' openers filed under this
// fallback regardless of their real alpha/beta/gamma content - a single
// shared misfiled thread, "topic:general" - because buildSegments always
// left the opener unlabeled; see s46t5-fix-report.txt for the per-segment
// trace that diagnosed it.
func acceptancePipelineFilesFourThreads(t *testing.T) {
	f := newAcceptanceFixture(t)
	for _, rec := range syntheticCorpus() {
		if _, err := f.threader.Route(context.Background(), acceptanceTurns(rec)); err != nil {
			t.Fatalf("Route(%s): %v", rec.ID, err)
		}
	}
	found := map[TopicType]ThreadID{}
	for _, topic := range syntheticTopics {
		id, ok, err := f.threadStore.LookupThread(context.Background(), TopicType(topic))
		if err != nil {
			t.Fatalf("LookupThread(%s): %v", topic, err)
		}
		if !ok {
			t.Fatalf("topic %q filed no thread after routing the full fixture", topic)
		}
		found[TopicType(topic)] = id
	}
	if len(found) != 4 {
		t.Fatalf("filed %d discrete topic threads, want exactly 4: %+v", len(found), found)
	}
	// "No extra or missing": the lookups above can only see misses, so the
	// total comes from the backing store, where a stray fifth thread shows.
	all, err := f.backing.ListThreads(context.Background())
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("backing store holds %d threads after routing the fixture, want exactly 4 (no extras): %+v", len(all), all)
	}
	for topic, id := range found {
		wantPrefix := topicThreadIDPrefix + string(topic)
		if string(id) != wantPrefix {
			t.Errorf("thread for topic %q has id %q, want the taxonomy-resolved %q", topic, id, wantPrefix)
		}
	}
}

// acceptanceObserveLogDefaultPresent proves the fresh-install default a
// new ObserveLogger starts in: with no persisted first_use_at row, the
// very first Observe call inside the 7-day window runs in observe mode
// (Observed=true, no thread touched) rather than silently applying
// routing - the "verified present" half of the ticket's Story 1 line.
// "Treated as graceful no-op when absent" (the other half) is
// acceptance_errors_test.go's ObserveLogAbsent case: an empty turn window
// is the no-op this line names (observe_log.go:163's own contract).
func acceptanceObserveLogDefaultPresent(t *testing.T) {
	f := newAcceptanceFixture(t)
	observer, err := NewObserveLogger(f.threader, f.kv, f.clock, f.bus)
	if err != nil {
		t.Fatalf("NewObserveLogger: %v", err)
	}
	result, err := observer.Observe(context.Background(), acceptanceTurns(syntheticCorpus()[0]))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !result.Observed {
		t.Fatalf("Observe result = %+v, want Observed=true: a fresh install with no first_use_at row "+
			"defaults to observe mode, never silently applying routing", result)
	}
	if len(result.ThreadIDs) != 0 {
		t.Fatalf("Observe result = %+v, want no ThreadIDs: observe mode never touches the ThreadStore", result)
	}
}

// acceptanceFixture, newAcceptanceFixture, acceptanceFallbackTopic,
// acceptanceLabels and acceptanceTurns live in
// acceptance_autofile_fixture_test.go (300-line split, that file's header).
