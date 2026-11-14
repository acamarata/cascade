package background

// scheduleQueueAdd handles the queue-add trigger path.
// It bypasses live-count checks, ignores MaxLive, accepts a nil live source,
// never calls LiveCount, and enqueues the item with its unchanged ID.
func scheduleQueueAdd(req ScheduleRequest, queue BackgroundEnqueuer) (bool, error) {
	if err := queue.Enqueue(req.ID); err != nil {
		return false, err
	}
	return true, nil
}
