// Purpose: Queue, the provider.Queue driver over a live Conn — the server
//   profile's Queue leg (P1-E17-W4-S38-T6), carrying the SAME contract the
//   local internal/storage/queue.Queue implements (at-least-once delivery
//   + visibility-timeout redelivery), proven equivalent by the same
//   storetest.RunQueueTests suite.
// Inputs: a *Conn, a Clock, and a Config (New) plus namespace/payload per
//   call.
// Outputs: a *provider.Message (Dequeue) or a *cascade.Error — never a raw
//   error crossing the provider boundary.
// Constraints: providers/** imports pkg/** only (Art.10.2), so this file
//   declares its own Clock seam (structurally identical to
//   internal/runtime.Clock) rather than importing it — the same pattern
//   providers/anthropic and its siblings already use; no bare time.Now
//   (Clock injection only). "Ready" ordering and inflight tracking use two
//   Redis ZSETs (score = sequence / visibility deadline) so a Dequeue's
//   sweep-then-claim needs no separate in-process index — the server IS
//   the shared state, unlike the local Store-backed driver's in-memory
//   namespaceState.
// SPORT: providers.redis.Queue/ADDED (P1-E17-W4-S38-T6).

package redis

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Clock is the subset of a clock this driver needs to resolve visibility
// deadlines: Now(). Structurally identical to internal/runtime.Clock and
// internal/testkit.FrozenClock — pass either one; this package never
// imports internal/ (Art.10.2).
type Clock interface {
	Now() time.Time
}

// Config bounds a Queue instance's capacity. Zero means unbounded.
type Config struct {
	// Capacity is the maximum number of un-acked messages (ready +
	// inflight) a namespace may hold before Enqueue starts returning
	// cascade.KindQuotaExhausted. Zero means unbounded.
	Capacity int
}

// Queue is the Redis-backed provider.Queue driver. The zero value is not
// usable; construct with NewQueue.
type Queue struct {
	conn  *Conn
	clock Clock
	cfg   Config
}

// NewQueue returns a Queue issuing every command over conn, resolving
// visibility deadlines against clock.
func NewQueue(conn *Conn, clock Clock, cfg Config) *Queue {
	return &Queue{conn: conn, clock: clock, cfg: cfg}
}

const queueKeyPrefix = "cascade:queue:"

func readyKey(ns string) string    { return queueKeyPrefix + ns + ":ready" }
func inflightKey(ns string) string { return queueKeyPrefix + ns + ":inflight" }
func seqKey(ns string) string      { return queueKeyPrefix + ns + ":seq" }
func payloadKey(ns, id string) string {
	return queueKeyPrefix + ns + ":payload:" + id
}
func receiptToIDKey(ns, receipt string) string {
	return queueKeyPrefix + ns + ":r2id:" + receipt
}

// idReceiptKey is the reverse of receiptToIDKey: id's currently-valid
// receipt, if any. claim sets both directions; requeueExpired (the
// ack-timeout path) and Ack/Nack both need this reverse lookup to remove
// the now-stale forward mapping too — without it, an old receipt would
// keep resolving via receiptToIDKey long after redelivery, since its
// entry's own Redis-native TTL is a cleanup safety net only, not the
// source of staleness truth (this driver's injected Clock is).
func idReceiptKey(ns, id string) string {
	return queueKeyPrefix + ns + ":receipt:" + id
}

// Capacity implements storetest.BoundedQueue.
func (q *Queue) Capacity(_ string) int { return q.cfg.Capacity }

// Enqueue implements provider.Queue.
func (q *Queue) Enqueue(ctx context.Context, namespace string, payload []byte) (string, error) {
	if q.cfg.Capacity > 0 {
		n, err := q.unackedCount(ctx, namespace)
		if err != nil {
			return "", err
		}
		if n >= int64(q.cfg.Capacity) {
			return "", cascade.Newf(cascade.KindQuotaExhausted, "redis.Queue.Enqueue: namespace %q at capacity %d", namespace, q.cfg.Capacity)
		}
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	seq, err := q.conn.rdb.Incr(ctx, seqKey(namespace)).Result()
	if err != nil {
		return "", cascade.Wrapf(classifyErr(err), err, "redis.Queue.Enqueue: namespace %q", namespace)
	}
	if err := q.conn.rdb.Set(ctx, payloadKey(namespace, id), payload, 0).Err(); err != nil {
		return "", cascade.Wrapf(classifyErr(err), err, "redis.Queue.Enqueue: storing payload in namespace %q", namespace)
	}
	if err := q.conn.rdb.ZAdd(ctx, readyKey(namespace), goredis.Z{Score: float64(seq), Member: id}).Err(); err != nil {
		return "", cascade.Wrapf(classifyErr(err), err, "redis.Queue.Enqueue: namespace %q", namespace)
	}
	return id, nil
}

// unackedCount reports the ready+inflight cardinality for namespace.
func (q *Queue) unackedCount(ctx context.Context, namespace string) (int64, error) {
	ready, err := q.conn.rdb.ZCard(ctx, readyKey(namespace)).Result()
	if err != nil {
		return 0, cascade.Wrapf(classifyErr(err), err, "redis.Queue: counting ready in namespace %q", namespace)
	}
	inflight, err := q.conn.rdb.ZCard(ctx, inflightKey(namespace)).Result()
	if err != nil {
		return 0, cascade.Wrapf(classifyErr(err), err, "redis.Queue: counting inflight in namespace %q", namespace)
	}
	return ready + inflight, nil
}

// Dequeue implements provider.Queue: it first sweeps any inflight message
// whose visibility deadline has elapsed back onto the ready set, then
// claims the lowest-sequence ready message, if any.
func (q *Queue) Dequeue(ctx context.Context, namespace string, visibilityTimeout time.Duration) (*provider.Message, error) {
	if err := q.sweepExpired(ctx, namespace); err != nil {
		return nil, err
	}
	res, err := q.conn.rdb.ZPopMin(ctx, readyKey(namespace), 1).Result()
	if err != nil {
		return nil, cascade.Wrapf(classifyErr(err), err, "redis.Queue.Dequeue: namespace %q", namespace)
	}
	if len(res) == 0 {
		return nil, nil
	}
	id, _ := res[0].Member.(string)
	return q.claim(ctx, namespace, id, visibilityTimeout)
}

// sweepExpired moves every inflight message whose deadline is at or before
// the clock's current instant back onto the ready set (at its original
// position is not preserved — it rejoins at the tail, ordered by this
// sweep's own sequence numbers), and drops its now-stale receipt mapping.
func (q *Queue) sweepExpired(ctx context.Context, namespace string) error {
	now := float64(q.clock.Now().UnixNano())
	upperBound := strconv.FormatFloat(now, 'f', -1, 64)
	expired, err := q.conn.rdb.ZRangeArgs(ctx, goredis.ZRangeArgs{
		Key: inflightKey(namespace), ByScore: true, Start: "-inf", Stop: upperBound,
	}).Result()
	if err != nil {
		return cascade.Wrapf(classifyErr(err), err, "redis.Queue: sweeping namespace %q", namespace)
	}
	for _, id := range expired {
		if err := q.requeueExpired(ctx, namespace, id); err != nil {
			return err
		}
	}
	return nil
}

// requeueExpired re-adds one expired id to the ready set, removes its
// inflight record, and drops its now-stale receipt mapping in both
// directions — so a later Ack/Nack against the old (pre-redelivery)
// receipt resolves to nothing and reports the ack-timeout error, exactly
// as if that receipt had never existed.
func (q *Queue) requeueExpired(ctx context.Context, namespace, id string) error {
	staleReceipt, err := q.conn.rdb.Get(ctx, idReceiptKey(namespace, id)).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return cascade.Wrapf(classifyErr(err), err, "redis.Queue: reading stale receipt for %q in namespace %q", id, namespace)
	}
	seq, err := q.conn.rdb.Incr(ctx, seqKey(namespace)).Result()
	if err != nil {
		return cascade.Wrapf(classifyErr(err), err, "redis.Queue: requeuing %q in namespace %q", id, namespace)
	}
	pipe := q.conn.rdb.TxPipeline()
	pipe.ZAdd(ctx, readyKey(namespace), goredis.Z{Score: float64(seq), Member: id})
	pipe.ZRem(ctx, inflightKey(namespace), id)
	pipe.Del(ctx, idReceiptKey(namespace, id))
	if staleReceipt != "" {
		pipe.Del(ctx, receiptToIDKey(namespace, staleReceipt))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return cascade.Wrapf(classifyErr(err), err, "redis.Queue: requeuing %q in namespace %q", id, namespace)
	}
	return nil
}

// claim records a fresh receipt for id, moves it into the inflight set
// with a deadline of now+visibilityTimeout, and returns the Message.
func (q *Queue) claim(ctx context.Context, namespace, id string, visibilityTimeout time.Duration) (*provider.Message, error) {
	payload, err := q.conn.rdb.Get(ctx, payloadKey(namespace, id)).Bytes()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return nil, cascade.Wrapf(classifyErr(err), err, "redis.Queue.Dequeue: reading payload %q in namespace %q", id, namespace)
	}
	receipt, err := randomID()
	if err != nil {
		return nil, err
	}
	deadline := float64(q.clock.Now().Add(visibilityTimeout).UnixNano())
	ttl := visibilityTimeout + time.Minute
	pipe := q.conn.rdb.TxPipeline()
	pipe.ZAdd(ctx, inflightKey(namespace), goredis.Z{Score: deadline, Member: id})
	pipe.Set(ctx, receiptToIDKey(namespace, receipt), id, ttl)
	pipe.Set(ctx, idReceiptKey(namespace, id), receipt, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, cascade.Wrapf(classifyErr(err), err, "redis.Queue.Dequeue: claiming %q in namespace %q", id, namespace)
	}
	return &provider.Message{ID: id, Payload: payload, Receipt: receipt}, nil
}

// resolveReceipt looks up the id a still-current receipt names. A missing
// mapping means the receipt is stale (already redelivered under a new
// receipt, or never claimed) — the ack-timeout error path.
func (q *Queue) resolveReceipt(ctx context.Context, namespace, receipt string) (string, error) {
	id, err := q.conn.rdb.Get(ctx, receiptToIDKey(namespace, receipt)).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return "", cascade.Newf(cascade.KindTimeout, "redis.Queue: receipt %q in namespace %q already redelivered or unknown", receipt, namespace)
		}
		return "", cascade.Wrapf(classifyErr(err), err, "redis.Queue: resolving receipt in namespace %q", namespace)
	}
	return id, nil
}

// Ack implements provider.Queue.
func (q *Queue) Ack(ctx context.Context, namespace, receipt string) error {
	id, err := q.resolveReceipt(ctx, namespace, receipt)
	if err != nil {
		return err
	}
	pipe := q.conn.rdb.TxPipeline()
	pipe.ZRem(ctx, inflightKey(namespace), id)
	pipe.Del(ctx, payloadKey(namespace, id))
	pipe.Del(ctx, receiptToIDKey(namespace, receipt))
	pipe.Del(ctx, idReceiptKey(namespace, id))
	if _, err := pipe.Exec(ctx); err != nil {
		return cascade.Wrapf(classifyErr(err), err, "redis.Queue.Ack: namespace %q", namespace)
	}
	return nil
}

// Nack implements provider.Queue: it releases id back to the ready set
// immediately, ahead of its visibility deadline.
func (q *Queue) Nack(ctx context.Context, namespace, receipt string) error {
	id, err := q.resolveReceipt(ctx, namespace, receipt)
	if err != nil {
		return err
	}
	seq, err := q.conn.rdb.Incr(ctx, seqKey(namespace)).Result()
	if err != nil {
		return cascade.Wrapf(classifyErr(err), err, "redis.Queue.Nack: namespace %q", namespace)
	}
	pipe := q.conn.rdb.TxPipeline()
	pipe.ZAdd(ctx, readyKey(namespace), goredis.Z{Score: float64(seq), Member: id})
	pipe.ZRem(ctx, inflightKey(namespace), id)
	pipe.Del(ctx, receiptToIDKey(namespace, receipt))
	pipe.Del(ctx, idReceiptKey(namespace, id))
	if _, err := pipe.Exec(ctx); err != nil {
		return cascade.Wrapf(classifyErr(err), err, "redis.Queue.Nack: namespace %q", namespace)
	}
	return nil
}

// randomID returns a 16-byte, hex-encoded random identifier, used for both
// message IDs and receipts. crypto/rand only — math/rand is banned outside
// tests (02 §v1.1).
func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "redis.Queue: generating random id")
	}
	return hex.EncodeToString(buf), nil
}
