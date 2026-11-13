package background

import (
	"math"
)

// BuildQueuedBrief builds a brief for a queued task and stores build errors as notes.
// It validates the queue, finds the task by ID, checks status,
// calls the builder once, and if building fails, stores the error in the task's Note field
// while keeping the task queued and unchanged otherwise.
// The original queue and unrelated tasks are never mutated.
func BuildQueuedBrief(q BriefQueue, id string, builder BriefBuilder) (BriefQueue, error) {
	if err := validateBriefQueue(q, id, builder); err != nil {
		return q, err
	}

	if q.StartSlotsUsed >= math.MaxInt || q.AccountSlotsUsed >= math.MaxInt {
		return q, ErrInvalidBriefQueue
	}

	taskIdx := -1
	for i := range q.Tasks {
		if q.Tasks[i].ID == id {
			taskIdx = i
			break
		}
	}
	if taskIdx == -1 {
		return q, ErrBriefTaskNotFound
	}

	if q.Tasks[taskIdx].Status != "queued" {
		return q, ErrInvalidBriefQueue
	}

	buildErr := builder.Build(q.Tasks[taskIdx].Brief)

	if buildErr != nil {
		newTasks := make([]BriefTask, len(q.Tasks))
		copy(newTasks, q.Tasks)
		newTasks[taskIdx].Note = buildErr.Error()
		return BriefQueue{
			Tasks:            newTasks,
			StartSlotsUsed:   q.StartSlotsUsed,
			AccountSlotsUsed: q.AccountSlotsUsed,
		}, nil
	}

	return q, nil
}
