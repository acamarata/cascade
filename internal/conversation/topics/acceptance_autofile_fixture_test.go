// Package topics (acceptance_autofile_fixture_test.go): Purpose:
// acceptanceFixture and its builder newAcceptanceFixture, plus the small
// helpers around it (acceptanceFallbackTopic, acceptanceLabels,
// acceptanceTurns) - split out of acceptance_autofile_test.go only to keep
// both files under the 300-line cap (Art.10.3), the same reason
// reassign_doubles_test.go and observe_doubles_test.go exist in this
// package. The subtests themselves stay in acceptance_autofile_test.go.
// SPORT: internal/conversation/topics acceptance (ADD) (P1-E21-W5-S46-T5).
package topics

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// acceptanceFixture wires one real composition (sqlite conversation.Store,
// events.Bus, ThreadStore, TaxonomyConfig, ExemplarStore, AutoThreader)
// for acceptance_autofile_test.go's subtests, mirroring cmd/cascade/
// chat_topics_engine_test.go's newChatTopicsFixture at the topics-package
// level (this ticket's files_scope has no door into cmd/cascade).
type acceptanceFixture struct {
	clock       *fixedClock
	kv          *storetest.MemStore
	bus         *events.Bus
	threadStore ThreadStore
	threader    *AutoThreader
	// backing is the real conversation store under threadStore, so the
	// four-threads assertion can count EVERY thread the pipeline filed,
	// not only the four it went looking for (independent review, A1).
	backing conversation.Store
}

func newAcceptanceFixture(t *testing.T) *acceptanceFixture {
	t.Helper()
	clock := newFixedClock()
	dbPath := filepath.Join(t.TempDir(), "acceptance-autofile.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := conversation.ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	store := conversation.NewStore(db)
	threadStore, err := NewConversationThreadStore(store, clock)
	if err != nil {
		t.Fatalf("NewConversationThreadStore: %v", err)
	}
	kv := storetest.NewMemStore()
	bus := events.New(kv, clock)
	// Fallback is "general": a real, honest name for "no recognized
	// topic" (acceptance_autofile_test.go's FALLBACK doc), not tuned to
	// any fixture content. With the D5 fix, syntheticCorpus's records all
	// open on real alpha/beta/gamma content that the classifier
	// recognizes, so this fallback is configured but never actually
	// reached by a successful run.
	taxonomy := NewTaxonomyConfig(acceptanceLabels(), acceptanceFallbackTopic)
	exemplars, err := NewExemplarStore(kv, clock, 0)
	if err != nil {
		t.Fatalf("NewExemplarStore: %v", err)
	}
	// classifier wraps the SAME keywordClassifier executor the Segmenter
	// below dispatches through, so the window's opener (classified here,
	// auto_thread.go's SEGMENT-ZERO IS CLASSIFIED HERE) and every other
	// turn in it (classified inside Segment) go through the identical
	// cheap-lane dispatch (segmenter_core.go's Classifier).
	classifier := NewClassifier(keywordClassifier{})
	seg, err := newTestSegmenter(keywordClassifier{}, onehotEmbedder{}, HysteresisConfig{Threshold: 0.5, Window: 2})
	if err != nil {
		t.Fatalf("NewSegmenter: %v", err)
	}
	threader, err := NewAutoThreader(seg, classifier, threadStore, taxonomy, exemplars, bus, clock)
	if err != nil {
		t.Fatalf("NewAutoThreader: %v", err)
	}
	return &acceptanceFixture{clock: clock, kv: kv, bus: bus, threadStore: threadStore, threader: threader, backing: store}
}

// acceptanceFallbackTopic is newAcceptanceFixture's TaxonomyConfig
// fallback: the TopicType any unrecognized label resolves to, including a
// classifier abstention on the window's opening segment
// (auto_thread.go's SEGMENT-ZERO IS CLASSIFIED HERE). "general" rather than
// one of syntheticTopics: with the D5 fix, an opener with real recognized
// content (every record in this fixture) never reaches the fallback at
// all, so there is no reason to tune it to the corpus - the pre-fix
// "alpha" fallback (rejected, s46t5-fix-report.txt) existed only to paper
// over the defect this fallback would otherwise have exposed.
const acceptanceFallbackTopic = TopicType("general")

// acceptanceLabels is the identity taxonomy over syntheticTopics: each
// vocabulary word resolves to the TopicType of the same name, so the
// resolved TopicType equals the classifier's raw label and
// topicThreadIDPrefix+topic is the thread id acceptancePipelineFilesFour
// Threads asserts against.
func acceptanceLabels() map[string]TopicType {
	labels := make(map[string]TopicType, len(syntheticTopics))
	for _, topic := range syntheticTopics {
		labels[topic] = TopicType(topic)
	}
	return labels
}

// acceptanceTurns rewrites rec's turns with a valid conversation.Role
// Speaker ("user"): syntheticCorpus's own turns carry the fixed marker
// Speaker "a" (segmenter_harness_test.go), which is sufficient for
// Segment/Evaluate (neither reads Speaker) but not for AppendTurn, which
// requires conversation.DecodeRole to succeed. Text, the only field either
// double reads, is untouched.
func acceptanceTurns(rec CorpusRecord) []Turn {
	turns := make([]Turn, len(rec.Turns))
	for i, turn := range rec.Turns {
		turns[i] = Turn{Speaker: "user", Text: turn.Text}
	}
	return turns
}
