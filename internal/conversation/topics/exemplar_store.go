// Purpose: ExemplarStore, the bounded per-topic FIFO of Turn exemplars
//   Reassign feeds and a future few-shot classify call (T2's cheap-lane
//   classifier) reads, persisted through the B/S-02 pkg/provider.Store
//   abstraction - no direct SQL, matching internal/context/summarizer_core.go's
//   precedent for the identical seam.
// Inputs: a provider.Store, a Clock, and a max-depth at construction; a
//   TopicType and Turn per Add call, a TopicType per Exemplars call.
// Outputs: the current per-topic exemplar slice, or a typed error for
//   caller misuse or a real storage failure.
// Constraints: no bare time.Now (Clock injected, matching this package's
//   established local-Clock convention - see internal/conversation/domain.go's
//   identical declaration and rationale). Persisted as one JSON-encoded
//   bounded slice per topic under the retrieval domain namespace
//   (internal/storage.DomainRetrieval) - "idempotent schema init on first
//   write" is Store.Put's own documented create-or-overwrite semantics
//   (pkg/provider/store.go); no separate schema-application call exists to
//   make idempotent because none is needed, the same posture
//   summarizer_core.go already takes for its own Store-backed records.
//
//   SCHEMA NOTE: the contract's prose schema (topic_type, exemplar_id,
//   text_hash, embedding_ref, created_at) has no field for the exemplar's
//   own turn content, yet Exemplars must return real Turn values for
//   few-shot injection - so exemplarRecord also carries Speaker/Text.
//   EmbeddingRef is always empty in this ticket: no Embedder is wired into
//   ExemplarStore's constructor (out of this ticket's scope, which is
//   internal/conversation/topics only), so the column exists per the
//   contract's schema but is never populated here - never a fabricated
//   reference, an honestly empty one a later ticket can fill.
// SPORT: internal/conversation/topics exemplar-store (ADD) (P1-E21-W5-S45-T3).

package topics

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Clock abstracts time.Now so ExemplarStore never reads the wall clock
// directly. Declared locally, duck-typed, matching
// internal/conversation/domain.go's identical Clock and its documented
// reasoning: any concrete Clock already in the tree satisfies this with
// zero adapter code.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// defaultExemplarDepth is the bounded FIFO's per-topic depth when
// NewExemplarStore is given maxDepth <= 0.
const defaultExemplarDepth = 20

// exemplarNamespace is the B/S-02 Store namespace every exemplar record is
// keyed under: storage.DomainRetrieval, per this ticket's own storage
// choice (table `topic_exemplars` in the retrieval domain).
const exemplarNamespace = string(storage.DomainRetrieval)

// exemplarRecord is the persisted shape of one exemplar. See this file's
// SCHEMA NOTE for why Speaker/Text are present alongside the contract's
// four named columns.
type exemplarRecord struct {
	TopicType    TopicType `json:"topic_type"`
	ExemplarID   string    `json:"exemplar_id"`
	TextHash     string    `json:"text_hash"`
	EmbeddingRef string    `json:"embedding_ref"`
	CreatedAt    int64     `json:"created_at"` // unix seconds, injected Clock
	Speaker      string    `json:"speaker"`
	Text         string    `json:"text"`
}

// ExemplarStore is the bounded per-topic FIFO of Turn exemplars. Build one
// with NewExemplarStore; the zero value is not usable.
type ExemplarStore struct {
	store    provider.Store
	clock    Clock
	maxDepth int
}

// NewExemplarStore validates its two required dependencies and returns a
// ready ExemplarStore. maxDepth <= 0 is not an error: it is treated as
// "use the contract's own default" (defaultExemplarDepth), since 0 or a
// negative depth is never a meaningful bound a caller intended.
func NewExemplarStore(store provider.Store, clock Clock, maxDepth int) (*ExemplarStore, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewExemplarStore: Store must not be nil")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "topics: NewExemplarStore: Clock must not be nil")
	}
	if maxDepth <= 0 {
		maxDepth = defaultExemplarDepth
	}
	return &ExemplarStore{store: store, clock: clock, maxDepth: maxDepth}, nil
}

// exemplarKey is the Store key one topic's whole bounded slice is written
// under, scoped by TopicType so distinct topics never collide.
func exemplarKey(topicType TopicType) string {
	return "topic_exemplars/" + string(topicType)
}

// Add appends turn as the newest exemplar for topicType, evicting the
// oldest entry when the bounded depth would otherwise be exceeded. Adding
// a turn this topic already holds an exemplar for is a no-op rather than a
// duplicate or an error: the record's id is a content address with no clock
// in it (newExemplarRecord), so a retried or repeated Add of the same turn
// converges on the one record the table's own PRIMARY KEY would allow. Add
// is
// read-modify-write against a single Store key: concurrent Add calls for
// the same topicType are not itself the concern this ticket's contract
// raises (ExemplarStore has no Tx-based CAS loop here, matching
// summarizer_core.go's own last-writer-wins posture for its per-key
// records).
func (s *ExemplarStore) Add(ctx context.Context, topicType TopicType, turn Turn) error {
	if ctx == nil {
		return cascade.New(cascade.KindInvalidInput, "topics: ExemplarStore.Add: ctx must not be nil")
	}
	if err := validateTopicType(topicType); err != nil {
		return err
	}
	records, err := s.load(ctx, topicType)
	if err != nil {
		return err
	}
	fresh := newExemplarRecord(topicType, turn, s.clock.Now())
	for _, r := range records {
		if r.ExemplarID == fresh.ExemplarID {
			return nil
		}
	}
	records = append(records, fresh)
	if len(records) > s.maxDepth {
		records = records[len(records)-s.maxDepth:]
	}
	return s.save(ctx, topicType, records)
}

// Exemplars returns the current bounded slice of Turn exemplars for
// topicType, oldest first, for few-shot injection into a future classify
// call. A topicType with no exemplars yet returns (nil, nil), not an
// error: an empty exemplar set is the expected state before the first Add.
func (s *ExemplarStore) Exemplars(ctx context.Context, topicType TopicType) ([]Turn, error) {
	if ctx == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "topics: ExemplarStore.Exemplars: ctx must not be nil")
	}
	if err := validateTopicType(topicType); err != nil {
		return nil, err
	}
	records, err := s.load(ctx, topicType)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	turns := make([]Turn, len(records))
	for i, r := range records {
		turns[i] = Turn{Speaker: r.Speaker, Text: r.Text}
	}
	return turns, nil
}

// load reads and JSON-decodes topicType's current record slice, treating a
// missing key as an empty slice rather than an error - the same
// found/not-found split summarizer_core.go's loadRecord already makes.
func (s *ExemplarStore) load(ctx context.Context, topicType TopicType) ([]exemplarRecord, error) {
	raw, err := s.store.Get(ctx, exemplarNamespace, exemplarKey(topicType))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var records []exemplarRecord
	if unmarshalErr := json.Unmarshal(raw, &records); unmarshalErr != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, unmarshalErr, "topics: exemplar store: decoding stored records")
	}
	return records, nil
}

// save JSON-encodes records and writes them to the store under topicType's
// key, creating or overwriting unconditionally (Store.Put's own contract) -
// this unconditional write is the "idempotent schema init on first write"
// this file's header documents.
func (s *ExemplarStore) save(ctx context.Context, topicType TopicType, records []exemplarRecord) error {
	raw, err := json.Marshal(records)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "topics: exemplar store: encoding records")
	}
	if err := s.store.Put(ctx, exemplarNamespace, exemplarKey(topicType), raw); err != nil {
		return err
	}
	return nil
}

// newExemplarRecord builds one exemplarRecord from turn at instant now.
// ExemplarID and TextHash are content-addressed (BLAKE3, matching
// internal/conversation/domain.go's contentAddress convention) and the
// CLOCK IS DELIBERATELY NOT PART OF ExemplarID: the id is a pure function
// of the turn's own content address and its topic, so a retried Add after a
// crash - or after any amount of wall time has passed - reproduces the
// identical record rather than a second copy of the same exemplar under a
// new id. That is also what lets Add enforce uniqueness in code, matching
// the PRIMARY KEY ("exemplar_id") in this table's reference migration
// (internal/retrieval/migrations/0020_topic_exemplars.sql). CreatedAt still
// records when the exemplar was first observed, which is data about the
// record, not part of its identity.
func newExemplarRecord(topicType TopicType, turn Turn, now time.Time) exemplarRecord {
	textHash := blake3Hex(turn.Speaker, turn.Text)
	return exemplarRecord{
		TopicType:  topicType,
		ExemplarID: blake3Hex(string(topicType), textHash),
		TextHash:   textHash,
		CreatedAt:  now.Unix(),
		Speaker:    turn.Speaker,
		Text:       turn.Text,
	}
}

// blake3Hex hashes its length-prefixed parts together (so "ab"+"c" and
// "a"+"bc" never collide) and returns the hex digest, matching
// internal/conversation/domain.go's contentAddress.
func blake3Hex(parts ...string) string {
	h := blake3.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(strconv.Itoa(len(p))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
