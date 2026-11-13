// Package nesting provides primitives for limiting concurrent background tasks.
package nesting

import "reflect"

// LaneWidth represents a concurrency limiter lane.
type LaneWidth interface {
	TryAcquire() bool
	Release()
}

// Task represents a background task with an ID and a nesting Depth.
type Task struct {
	ID    string
	Depth int
}

// Limiter manages task admission based on depth, capacity, and queue state.
type Limiter struct {
	lane    LaneWidth
	running int
	queue   []Task
}

// NewLimiter creates a new Limiter with zero running tasks and an empty queue.
func NewLimiter(lane LaneWidth) *Limiter {
	return &Limiter{
		lane:    lane,
		running: 0,
		queue:   nil,
	}
}

// isNilLane checks if the lane is an untyped nil or a typed nil.
func isNilLane(lane LaneWidth) bool {
	if lane == nil {
		return true
	}
	v := reflect.ValueOf(lane)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return v.IsNil()
	default:
		return false
	}
}

// Admit attempts to admit a task. It returns "running" and nil if the task starts immediately,
// or "queued" and nil if it is appended to the FIFO queue. It never returns a non-nil error
// for a non-nil limiter.
func (l *Limiter) Admit(task Task) (string, error) {
	depth := task.Depth
	if depth < 0 {
		depth = 0
	}

	if len(l.queue) > 0 || depth > 10 || l.running >= 400 || isNilLane(l.lane) {
		l.queue = append(l.queue, task)
		return "queued", nil
	}

	if l.lane.TryAcquire() {
		l.running++
		return "running", nil
	}

	l.queue = append(l.queue, task)
	return "queued", nil
}

// Running returns the number of currently active tasks.
func (l *Limiter) Running() int {
	return l.running
}

// Queued returns an independent slice copy of the queued tasks in FIFO order.
// It returns nil when empty.
func (l *Limiter) Queued() []Task {
	if len(l.queue) == 0 {
		return nil
	}
	res := make([]Task, len(l.queue))
	copy(res, l.queue)
	return res
}
