// Purpose: message-ID and receipt generation for Queue. IDs are stable
//   across a message's whole lifetime (redeliveries keep their ID);
//   receipts are minted fresh on every Dequeue claim so a stale receipt
//   from an earlier delivery can always be told apart from the current one
//   (pkg/provider/queue.go's Message.Receipt doc comment).
// Constraints: crypto/rand only (Art.7.3 forbids unseeded math/rand in
//   domain logic; crypto/rand is exempt — it is never seeded, by design,
//   and forbidigo's rule only names the math/rand package selector). A
//   monotonic in-memory sequence is combined with random bytes so IDs stay
//   sortable in enqueue order within one Queue instance's lifetime while
//   remaining collision-free even if two Queue instances share one Store
//   (a documented, accepted scope limit — see New's doc comment).
// SPORT: internal.storage.queue.Queue/ADDED (P1-E02-W1-S02-T4).

package queue

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"

	"github.com/acamarata/cascade/pkg/cascade"
)

// idSuffixBytes is how many random bytes follow the monotonic sequence in
// a generated message ID, to keep IDs collision-free across Queue
// instances that happen to share one Store.
const idSuffixBytes = 4

// receiptBytes is how many random bytes make up a generated receipt token.
const receiptBytes = 16

// generateID returns a new, sortable-by-enqueue-order message ID: a
// zero-padded monotonic sequence number (from seq) followed by a random
// hex suffix.
func generateID(seq *atomic.Uint64) (string, error) {
	n := seq.Add(1)
	suffix := make([]byte, idSuffixBytes)
	if _, err := cryptorand.Read(suffix); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "queue: generating message id")
	}
	return fmt.Sprintf("%020d-%s", n, hex.EncodeToString(suffix)), nil
}

// generateReceipt returns a new random receipt token, distinct from every
// previously issued receipt with overwhelming probability.
func generateReceipt() (string, error) {
	b := make([]byte, receiptBytes)
	if _, err := cryptorand.Read(b); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "queue: generating receipt")
	}
	return hex.EncodeToString(b), nil
}
