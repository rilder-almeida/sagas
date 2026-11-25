package sagas

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

var (
	// ErrJournal reports a failed Append whose error is not ErrExecutionExists, not ErrConcurrentWriter, and not the
	// error of the context of the caller after the grace period ends.
	ErrJournal = &KindError{name: "journal", message: "sagas: journal failure"}
	// ErrExecutionExists is returned when the journal rejects the first event because it already holds events for it.
	ErrExecutionExists = &KindError{name: "execution-exists", message: "sagas: execution exists"}
	// ErrConcurrentWriter is returned when the journal rejects a later event because another writer appended first.
	ErrConcurrentWriter = &KindError{name: "concurrent-writer", message: "sagas: concurrent writer"}
	// ErrNoEvents is returned by EventReader.Events when the journal holds no event for the execution.
	ErrNoEvents = &KindError{name: "no-events", message: "sagas: no events"}
)

// ErrInvalidEvent reports an event that CheckEvent refuses.
var ErrInvalidEvent = &KindError{name: "invalid-event", message: "sagas: invalid event"}

// Journal stores the events of each execution and only appends them. Append stores the next Seq of an execution as a
// deep copy of the event and refuses any other Seq. An equal resend by SameEvent stores nothing and returns nil. An
// event survives a crash of the process once Append returns nil. Append refuses an event that CheckEvent refuses. Once
// ctx is done, Append stores nothing and returns an error. sagastest.Journal proves this contract.
type Journal interface {
	Append(ctx context.Context, e *Event) error
}

// EventReader reads the events of an execution in Seq order.
type EventReader interface {
	Events(ctx context.Context, execution string) ([]Event, error)
}

var _ EventReader = (*MemoryJournal)(nil)

// MemoryJournal is a Journal that keeps every execution in memory. Its zero value is ready to use.
type MemoryJournal struct {
	mu      sync.Mutex
	streams map[string][]Event
}

// NewMemoryJournal returns an empty MemoryJournal.
func NewMemoryJournal() *MemoryJournal {
	return &MemoryJournal{streams: map[string][]Event{}}
}

// Append implements Journal.
func (j *MemoryJournal) Append(ctx context.Context, e *Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := CheckEvent(e); err != nil {
		return err
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	if j.streams == nil {
		j.streams = map[string][]Event{}
	}

	stream := j.streams[e.Execution]
	if e.Seq == len(stream)+1 {
		stored := *e
		cloneSlices(&stored)
		j.streams[e.Execution] = append(stream, stored)

		return nil
	}

	if e.Seq <= len(stream) && SameEvent(&stream[e.Seq-1], e) {
		return nil
	}

	rejection := ErrConcurrentWriter
	if e.Seq == 1 {
		rejection = ErrExecutionExists
	}

	return fmt.Errorf("%w: execution %q seq %d", rejection, e.Execution, e.Seq)
}

// Events implements EventReader.
func (j *MemoryJournal) Events(ctx context.Context, execution string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	stream := j.streams[execution]
	if len(stream) == 0 {
		return nil, fmt.Errorf("%w: execution %q", ErrNoEvents, execution)
	}

	return copyEvents(stream), nil
}

func copyEvents(events []Event) []Event {
	out := make([]Event, len(events))
	copy(out, events)

	for i := range out {
		cloneSlices(&out[i])
	}

	return out
}

func cloneSlices(e *Event) {
	e.Changed = slices.Clone(e.Changed)
	e.Steps = slices.Clone(e.Steps)
	e.Err.Previous = slices.Clone(e.Err.Previous)
}

// CheckEvent returns an error that wraps ErrInvalidEvent for a nil event, a Seq below 1 or an incompatible Version.
func CheckEvent(e *Event) error {
	switch {
	case e == nil:
		return fmt.Errorf("%w: nil event", ErrInvalidEvent)
	case e.Seq < 1:
		return fmt.Errorf("%w: execution %q seq %d", ErrInvalidEvent, e.Execution, e.Seq)
	case !compatibleVersion(e.Version):
		return fmt.Errorf("%w: execution %q seq %d version %q is not compatible with %q",
			ErrInvalidEvent, e.Execution, e.Seq, e.Version, EventVersion)
	default:
		return nil
	}
}

// SameEvent reports whether sent is an equal resend of stored, with Time as an instant and a nil slice as empty.
func SameEvent(stored, sent *Event) bool {
	return sameScalars(stored, sent) && sameError(&stored.Err, &sent.Err) &&
		slices.Equal(stored.Changed, sent.Changed) && slices.Equal(stored.Steps, sent.Steps) &&
		stored.Time.Equal(sent.Time)
}

func sameError(a, b *EventError) bool {
	return a.Kind == b.Kind && a.Message == b.Message && a.Cause == b.Cause && slices.Equal(a.Previous, b.Previous)
}

func sameScalars(a, b *Event) bool {
	return a.Saga == b.Saga && a.Execution == b.Execution && a.Writer == b.Writer && a.Seq == b.Seq &&
		a.Version == b.Version && a.Kind == b.Kind && a.Step == b.Step && a.Attempts == b.Attempts &&
		a.Status == b.Status
}
