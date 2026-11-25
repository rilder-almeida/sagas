package sagas

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

var (
	// ErrJournal wraps an Append error that is neither a rejection nor ctx's own error after the grace end.
	ErrJournal = &KindError{name: "journal", message: "sagas: journal failure"}
	// ErrExecutionExists is returned when the journal rejects the first event because it already holds events for it.
	ErrExecutionExists = &KindError{name: "execution_exists", message: "sagas: execution exists"}
	// ErrConcurrentWriter is returned when the journal rejects a later event because another writer appended first.
	ErrConcurrentWriter = &KindError{name: "concurrent_writer", message: "sagas: concurrent writer"}
	// ErrNoEvents is returned by EventReader.Events when the journal holds no event for the execution.
	ErrNoEvents = &KindError{name: "no_events", message: "sagas: no events"}
)

var errInvalidEvent = errors.New("sagas: invalid event")

// Journal stores the events of each execution and only appends them. Append treats a nil event, a Seq below 1 or a
// Version other than EventVersion as invalid. It stores the next Seq of an execution as a deep copy of the event. An
// equal resend from the same Writer stores nothing and returns nil, and Append refuses any other Seq. Once ctx is done,
// it stores nothing and returns an error. sagastest.Journal proves this contract.
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

	if e == nil {
		return fmt.Errorf("%w: nil event", errInvalidEvent)
	}

	if e.Seq < 1 {
		return fmt.Errorf("%w: execution %q seq %d", errInvalidEvent, e.Execution, e.Seq)
	}

	if e.Version != EventVersion {
		return fmt.Errorf("%w: execution %q seq %d version %q", errInvalidEvent, e.Execution, e.Seq, e.Version)
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

	if e.Seq <= len(stream) && sameEvent(&stream[e.Seq-1], e) {
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

func sameEvent(a, b *Event) bool {
	return sameScalars(a, b) && sameError(&a.Err, &b.Err) && slices.Equal(a.Changed, b.Changed) &&
		slices.Equal(a.Steps, b.Steps) && a.Time.Equal(b.Time)
}

func sameError(a, b *EventError) bool {
	return a.Kind == b.Kind && a.Message == b.Message && a.Cause == b.Cause && slices.Equal(a.Previous, b.Previous)
}

func sameScalars(a, b *Event) bool {
	return a.Saga == b.Saga && a.Execution == b.Execution && a.Writer == b.Writer && a.Seq == b.Seq &&
		a.Version == b.Version && a.Kind == b.Kind && a.Step == b.Step && a.Attempt == b.Attempt && a.Status == b.Status
}
