// Package lessons provides a lesson pipeline that transforms requests.
package lessons

import "errors"

// ErrUnknownCaller is returned when a caller is not recognized.
var ErrUnknownCaller = errors.New("unknown caller")

// ErrInvalidLesson is returned when a nil lesson is encountered.
var ErrInvalidLesson = errors.New("invalid lesson")

// ErrInvalidApplier is returned when an applier is nil or invalid.
var ErrInvalidApplier = errors.New("invalid applier")

// Request represents the input and output of a lesson transformation.
type Request struct {
	// Text is the payload carried through the pipeline.
	Text string
}

// Lesson is a function that transforms a Request.
type Lesson func(Request) Request

// Hook is an optional callback invoked after lessons are applied.
type Hook interface {
	// Applied is called with the identifier of the caller that triggered the pipeline.
	Applied(caller string)
}

// Applier holds a pipeline of lessons and an optional hook.
type Applier struct {
	lessons []Lesson
	hook    Hook
	invalid bool
}

// New creates an Applier from the given lessons and hook.
// It defensively copies the slice, preserving order and duplicates.
// A nil or empty slice produces an intentional empty pipeline.
// Nil Lesson elements are recorded as invalid configuration.
// A nil or typed-nil Hook is treated as an optional no-op.
func New(lessons []Lesson, h Hook) *Applier {
	copied := make([]Lesson, len(lessons))
	invalid := false
	for i, l := range lessons {
		if l == nil {
			invalid = true
		}
		copied[i] = l
	}

	var hook Hook
	if h != nil {
		switch v := any(h).(type) {
		case interface{ IsNil() bool }:
			if !v.IsNil() {
				hook = h
			}
		default:
			hook = h
		}
	}

	return &Applier{
		lessons: copied,
		hook:    hook,
		invalid: invalid,
	}
}

// apply runs the lesson pipeline for the given caller and request.
func (a *Applier) apply(caller string, r Request) (Request, error) {
	if a == nil {
		return r, ErrInvalidApplier
	}
	if a.invalid {
		return r, ErrInvalidLesson
	}
	for _, l := range a.lessons {
		r = l(r)
	}
	if a.hook != nil {
		a.hook.Applied(caller)
	}
	return r, nil
}

// Ask applies the lesson pipeline on behalf of the "ask" caller.
func (a *Applier) Ask(r Request) (Request, error) {
	return a.apply("ask", r)
}

// Forge applies the lesson pipeline on behalf of the "forge" caller.
func (a *Applier) Forge(r Request) (Request, error) {
	return a.apply("forge", r)
}
