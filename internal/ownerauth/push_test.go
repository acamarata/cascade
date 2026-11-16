package ownerauth

import (
	"testing"
)

func TestOwnerQueueItemFieldsExist(t *testing.T) {
	item := OwnerQueueItem{
		SHA:         "abc123",
		CommitCount: 5,
	}
	if item.CommitCount != 5 {
		t.Errorf("expected CommitCount 5, got %d", item.CommitCount)
	}
}
