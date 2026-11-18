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
	// ErrJournal wraps an Append error that is not a rejection; a Journal returns its own failures as is.
	ErrJournal = errors.New("sagas: journal failure")
	// ErrExecutionExists is returned when the journal rejects the first event because the execution already exists.
	ErrExecutionExists = errors.New("sagas: execution exists")
	// ErrConcurrentWriter is returned when the journal rejects a later event because another writer appended first.
	ErrConcurrentWriter = errors.New("sagas: concurrent writer")

	errInvalidEvent = errors.New("sagas: invalid event")
)

// Journal records events append only and never delivers work, removes or marks an event. Append stores a copy of e,
// never e or its slices, only when its Seq is its execution's last Seq plus one; an equal resend from the same Writer
// (slices by content, Time by Time.Equal) returns nil and writes nothing. Any other Seq of 1 or more is rejected,
// wrapping ErrExecutionExists at Seq 1 and ErrConcurrentWriter later; a nil e or a Seq below 1 is an invalid event.
type Journal interface {
	Append(ctx context.Context, e *Event) error
}

// Event is one record of an execution, numbered by Seq from 1 and tagged with the Writer token of its run.
type Event struct {
	Saga      string
	Execution string
	Writer    string
	Seq       int
	Kind      EventKind
	Step      string
	Attempt   int
	Err       string
	Changed   []string
	Steps     []StepZone
	Status    Status
	Time      time.Time
}

// StepZone names a step and the zone it runs in.
type StepZone struct {
	Name string
	Zone Zone
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

type memoryJournal struct {
	mu      sync.Mutex
	streams map[string][]Event
}

func newMemoryJournal() *memoryJournal {
	return &memoryJournal{streams: map[string][]Event{}}
}

func (j *memoryJournal) Append(_ context.Context, e *Event) error {
	if e == nil {
		return fmt.Errorf("%w: nil event", errInvalidEvent)
	}

	if e.Seq < 1 {
		return fmt.Errorf("%w: execution %q seq %d", errInvalidEvent, e.Execution, e.Seq)
	}

	j.mu.Lock()
	defer j.mu.Unlock()

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

func (j *memoryJournal) events(execution string) []Event {
	j.mu.Lock()
	defer j.mu.Unlock()

	return copyEvents(j.streams[execution])
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
}

func sameEvent(a, b *Event) bool {
	return sameScalars(a, b) && slices.Equal(a.Changed, b.Changed) && slices.Equal(a.Steps, b.Steps) &&
		a.Time.Equal(b.Time)
}

func sameScalars(a, b *Event) bool {
	return a.Saga == b.Saga && a.Execution == b.Execution && a.Writer == b.Writer && a.Seq == b.Seq &&
		a.Kind == b.Kind && a.Step == b.Step && a.Attempt == b.Attempt && a.Err == b.Err && a.Status == b.Status
}
