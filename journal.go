package sagas

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

var (
	// ErrJournal wraps an Append error that is neither a rejection nor ctx's own error after the grace end.
	ErrJournal = &KindError{name: "journal", message: "sagas: journal failure"}
	// ErrExecutionExists is returned when the journal rejects the first event because the execution already exists.
	ErrExecutionExists = &KindError{name: "execution_exists", message: "sagas: execution exists"}
	// ErrConcurrentWriter is returned when the journal rejects a later event because another writer appended first.
	ErrConcurrentWriter = &KindError{name: "concurrent_writer", message: "sagas: concurrent writer"}
	// ErrNoEvents is returned by EventReader.Events when the journal holds no event for the execution.
	ErrNoEvents = &KindError{name: "no_events", message: "sagas: no events"}
)

var errInvalidEvent = errors.New("sagas: invalid event")

// Journal only appends events and never delivers work, removes or marks one. A nil e, a Seq below 1 or a Version not
// EventVersion is invalid. Append stores a deep copy of every field of e, never e or its slices, when e.Seq is its
// execution's last Seq plus one; an equal resend from the same Writer (slices by content, Time by Time.Equal) returns
// nil and writes nothing. Any other Seq is rejected, wrapping ErrExecutionExists at Seq 1 and ErrConcurrentWriter
// later. Once ctx is done, Append stores nothing and returns an error. Rerun sagastest.Journal on each upgrade.
type Journal interface {
	Append(ctx context.Context, e *Event) error
}

// EventReader gives an execution's events by Seq, or an error wrapping ctx.Err() if ctx is done or ErrNoEvents if none.
type EventReader interface {
	Events(ctx context.Context, execution string) ([]Event, error)
}

var _ EventReader = (*MemoryJournal)(nil)

// EventVersion is the format of the events this release writes, stored in Event.Version, which a reader checks
// first. Version 1 events hold no state values, so a version 1 execution cannot be resumed.
const EventVersion = 1

// Event is one record of an execution, numbered by Seq from 1 and tagged with the Writer token of its run.
type Event struct {
	Saga      string
	Execution string
	Writer    string
	Seq       int
	Version   int
	Kind      EventKind
	Step      string
	Attempt   int
	Err       EventError
	Changed   []string
	Steps     []StepZone
	Status    Status
	Time      time.Time
}

// EventError is the text form of an Error as a journal stores it. The zero value means no error.
type EventError struct {
	Kind     string
	Message  string
	Cause    string
	Previous []string
}

// StepZone names a step and the zone it runs in. Compensable is true when the step has a compensation.
type StepZone struct {
	Name        string
	Zone        Zone
	Compensable bool
}

// EventKind says what an Event records. The empty string means not set and is never a real event.
type EventKind string

// EventKind values.
const (
	EventSagaStarted           EventKind = "saga-started"
	EventStepStarted           EventKind = "step-started"
	EventStepSucceeded         EventKind = "step-succeeded"
	EventStepFailed            EventKind = "step-failed"
	EventStepUnknown           EventKind = "step-unknown"
	EventStepSkipped           EventKind = "step-skipped"
	EventCompensationStarted   EventKind = "compensation-started"
	EventCompensationSucceeded EventKind = "compensation-succeeded"
	EventCompensationFailed    EventKind = "compensation-failed"
	EventCompensationBlocked   EventKind = "compensation-blocked"
	EventCompensationHalted    EventKind = "compensation-halted"
	EventSagaFinished          EventKind = "saga-finished"
)

// Zone says whether a step can be compensated: before the pivot, the pivot itself, or after it. The empty string
// means not set and is never a real zone.
type Zone string

// Zone values.
const (
	ZoneCompensable Zone = "compensable"
	ZonePivot       Zone = "pivot"
	ZoneForwardOnly Zone = "forward-only"
)

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
		return fmt.Errorf("%w: execution %q seq %d version %d", errInvalidEvent, e.Execution, e.Seq, e.Version)
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
