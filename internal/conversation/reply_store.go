package conversation

// Purpose: the storage half of chat.reply: the conversation_reply mapping
//   (one assistant turn per user turn), the one-transaction write of an
//   assistant turn with its mapping, the turn lookup chat.reply validates
//   against, and the Summarizer's process-local summary cache.
// Inputs: the SQL conversation store; turn ids.
// Outputs: typed pkg/cascade errors; a duplicate mapping is ErrImmutable.
// Constraints: the assistant turn, its segments and its mapping row commit
//   in ONE transaction, so a stored reply always has its mapping and a
//   mapping always names a stored turn.
// SPORT: internal.conversation.reply_store/ADDED (P1-CPA-16).

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// tableReply maps a user turn to the one assistant turn that answers it.
const tableReply = "conversation_reply"

// summaryCacheMax bounds the Summarizer's cache; a full cache starts over.
const summaryCacheMax = 512

var (
	errReplyStore      = cascade.New(cascade.KindInvalidInput, "conversation: chat.reply needs the SQL conversation store")
	errSummaryNotFound = cascade.New(cascade.KindNotFound, "conversation: summary not cached")
	errSummaryCAS      = cascade.New(cascade.KindConflict, "conversation: summary changed concurrently")
)

// replyStep is MigrationSet's step for the reply mapping table.
func replyStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "conversation_reply: the assistant turn answering each user turn",
		Table: &migrate.TableDef{
			Name:    tableReply,
			Columns: []migrate.ColumnDef{id("user_turn_id"), text("assistant_turn_id")},
		},
	}
}

// replyTurnRef is the part of a stored turn chat.reply validates.
type replyTurnRef struct {
	ThreadID string
	Role     Role
}

// lookupTurn reads turnID's thread and role; ok is false for no such turn.
func (s *conversationStore) lookupTurn(ctx context.Context, turnID string) (replyTurnRef, bool, error) {
	var ref replyTurnRef
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT thread_id, role FROM `+tableTurn+` WHERE id = ?`, turnID).
		Scan(&ref.ThreadID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return replyTurnRef{}, false, nil
	}
	if err != nil {
		return replyTurnRef{}, false, cascade.Wrap(cascade.KindUnavailable, err, "conversation: look up turn")
	}
	ref.Role = Role(role)
	return ref, true, nil
}

// replyFor returns the assistant turn stored for userTurnID, if any.
func (s *conversationStore) replyFor(ctx context.Context, userTurnID string) (string, bool, error) {
	var assistant string
	err := s.db.QueryRowContext(ctx,
		`SELECT assistant_turn_id FROM `+tableReply+` WHERE user_turn_id = ?`, userTurnID).Scan(&assistant)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindUnavailable, err, "conversation: read reply mapping")
	}
	return assistant, true, nil
}

// appendReply commits turn, its segments and the userTurnID -> turn.ID
// mapping in one transaction. A second reply for the same user turn fails
// on the mapping's primary key and rolls the turn back with it.
func (s *conversationStore) appendReply(ctx context.Context, userTurnID string, turn Turn, segments []Segment) error {
	if userTurnID == "" {
		return ErrInvalidRecord
	}
	if err := validateTurn(turn); err != nil {
		return err
	}
	for _, seg := range segments {
		if err := validateSegment(seg); err != nil {
			return err
		}
	}
	fts := s.hasFTS(ctx) // probed before the tx opens: see AppendSegment
	return runTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := appendTurnTx(ctx, tx, turn); err != nil {
			return err
		}
		for _, seg := range segments {
			if err := insertSegmentTx(ctx, tx, seg, turn.ThreadID, fts); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO `+tableReply+` (user_turn_id, assistant_turn_id) VALUES (?, ?)`, userTurnID, turn.ID)
		return translateAppendError(err, "conversation: append reply mapping")
	})
}

// summaryCache is the Summarizer's provider.Store: rolling summaries of
// reply history slots, held in this process only and bounded at
// summaryCacheMax entries (a full cache is cleared; summaries regenerate).
type summaryCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

var _ provider.Store = (*summaryCache)(nil)

func newSummaryCache() *summaryCache { return &summaryCache{data: map[string][]byte{}} }

func cacheKey(namespace, key string) string { return namespace + "\x00" + key }

// Get implements provider.Store.
func (c *summaryCache) Get(_ context.Context, namespace, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getLocked(cacheKey(namespace, key))
}

// Put implements provider.Store.
func (c *summaryCache) Put(_ context.Context, namespace, key string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(cacheKey(namespace, key), value)
	return nil
}

// Delete implements provider.Store.
func (c *summaryCache) Delete(_ context.Context, namespace, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, cacheKey(namespace, key))
	return nil
}

// Scan implements provider.Store over a snapshot taken at call time.
func (c *summaryCache) Scan(_ context.Context, namespace, prefix string) (provider.Iterator, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it := &cacheIterator{pos: -1}
	full := cacheKey(namespace, prefix)
	for k, v := range c.data {
		if strings.HasPrefix(k, full) {
			it.keys = append(it.keys, strings.TrimPrefix(k, cacheKey(namespace, "")))
			it.vals = append(it.vals, append([]byte(nil), v...))
		}
	}
	sort.Sort(it)
	return it, nil
}

// Tx implements provider.Store: fn runs under the cache lock and every
// write it made is undone when it returns an error.
func (c *summaryCache) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx := &cacheTx{c: c, undo: map[string][]byte{}}
	if err := fn(ctx, tx); err != nil {
		for k, v := range tx.undo {
			if v == nil {
				delete(c.data, k)
			} else {
				c.data[k] = v
			}
		}
		return err
	}
	return nil
}

func (c *summaryCache) getLocked(k string) ([]byte, error) {
	v, ok := c.data[k]
	if !ok {
		return nil, errSummaryNotFound
	}
	return append([]byte(nil), v...), nil
}

func (c *summaryCache) putLocked(k string, value []byte) {
	if _, ok := c.data[k]; !ok && len(c.data) >= summaryCacheMax {
		c.data = map[string][]byte{}
	}
	c.data[k] = append([]byte{}, value...)
}

// cacheTx is summaryCache's provider.Tx; undo holds each touched key's
// prior value (nil: absent).
type cacheTx struct {
	c    *summaryCache
	undo map[string][]byte
}

func (t *cacheTx) remember(k string) {
	if _, seen := t.undo[k]; !seen {
		t.undo[k] = t.c.data[k]
	}
}

func (t *cacheTx) Get(_ context.Context, namespace, key string) ([]byte, error) {
	return t.c.getLocked(cacheKey(namespace, key))
}

func (t *cacheTx) Put(_ context.Context, namespace, key string, value []byte) error {
	k := cacheKey(namespace, key)
	t.remember(k)
	t.c.data[k] = append([]byte{}, value...)
	return nil
}

func (t *cacheTx) Delete(_ context.Context, namespace, key string) error {
	k := cacheKey(namespace, key)
	t.remember(k)
	delete(t.c.data, k)
	return nil
}

// CompareAndSwap writes newValue when the current value equals old; a nil
// old means the key must be absent.
func (t *cacheTx) CompareAndSwap(ctx context.Context, namespace, key string, old, newValue []byte) error {
	cur, ok := t.c.data[cacheKey(namespace, key)]
	if (old == nil && ok) || (old != nil && (!ok || !bytes.Equal(cur, old))) {
		return errSummaryCAS
	}
	return t.Put(ctx, namespace, key, newValue)
}

// cacheIterator walks a Scan snapshot in key order.
type cacheIterator struct {
	keys []string
	vals [][]byte
	pos  int
}

func (it *cacheIterator) Len() int           { return len(it.keys) }
func (it *cacheIterator) Less(i, j int) bool { return it.keys[i] < it.keys[j] }
func (it *cacheIterator) Swap(i, j int) {
	it.keys[i], it.keys[j] = it.keys[j], it.keys[i]
	it.vals[i], it.vals[j] = it.vals[j], it.vals[i]
}

func (it *cacheIterator) Next(_ context.Context) bool { it.pos++; return it.pos < len(it.keys) }
func (it *cacheIterator) Key() string                 { return it.keys[it.pos] }
func (it *cacheIterator) Value() []byte               { return it.vals[it.pos] }
func (it *cacheIterator) Err() error                  { return nil }
func (it *cacheIterator) Close() error                { return nil }
