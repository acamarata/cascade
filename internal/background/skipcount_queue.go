package background

// handleQueueAdd schedules a queue-add trigger by enqueuing the item directly.
// It bypasses live count checks, ignores MaxLive, and accepts a nil LiveCountSource.
func handleQueueAdd(req ScheduleRequest, queue BackgroundEnqueuer) (bool, error) {
	err := queue.Enqueue(req.ID)
	if err != nil {
		return false, err
	}
	return true, nil
}
