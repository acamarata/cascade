// Package ownerauth provides core types for owner authentication and approval workflows.
package ownerauth

import (
	"context"
)

// ScanSummary represents the result of a security scan over pushed commits.
type ScanSummary struct {
	// Clean indicates whether the scan found no issues.
	Clean bool
}

// OwnerQueueItem represents a single item in the owner's push queue.
type OwnerQueueItem struct {
	// SHA is the commit hash associated with the push.
	SHA string

	// CommitCount is the number of commits included in the push.
	CommitCount int

	// ScanResult contains the security scan summary for the pushed commits.
	ScanResult ScanSummary
}

// PushRequest encapsulates the data required to push code changes to a remote repository.
type PushRequest struct {
	// RepoID is the identifier of the target repository.
	RepoID string

	// Branch is the target branch for the push.
	Branch string

	// Item is the queue item being pushed.
	Item OwnerQueueItem
}

// DaemonPusher defines the interface for pushing owner-approved changes to a remote.
type DaemonPusher interface {
	// Push sends a push request to the remote repository.
	Push(ctx context.Context, req PushRequest) error
}
