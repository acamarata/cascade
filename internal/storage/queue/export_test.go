// Purpose: the canonical Go "export_test.go" seam — compiled only for `go
//   test` (it is a _test.go file, so it never ships in a production
//   binary) — that lets the external queue_test package reach
//   generateReceipt's otherwise-unexported entropy source. Exists solely
//   for TestP1QueueStorageFaults's receipt-generation fault subtest
//   (fault_test.go/retry_fault_test.go); never called from production code.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue

import (
	"io"
	"testing"
)

// SetReceiptEntropyForTest swaps generateReceipt's entropy source to r for
// the duration of t, restoring the real crypto/rand.Reader via t.Cleanup.
func SetReceiptEntropyForTest(t *testing.T, r io.Reader) {
	t.Helper()
	prev := receiptEntropy
	receiptEntropy = r
	t.Cleanup(func() { receiptEntropy = prev })
}
