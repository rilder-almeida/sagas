package sagas

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"
)

var (
	// ErrJournal is returned when a journal append fails for any reason other than a rejection.
	ErrJournal = errors.New("sagas: journal failure")
	// ErrExecutionExists is returned when the journal rejects the first event because the execution already exists.
	ErrExecutionExists = errors.New("sagas: execution exists")
	// ErrConcurrentWriter is returned when the journal rejects a later event because another writer appended first.
	ErrConcurrentWriter = errors.New("sagas: concurrent writer")

	errInvalidSeq = errors.New("sagas: invalid event sequence")
)

// Journal records events append only and never delivers work, removes or marks an event. Append writes an event
// only when its Seq is its execution's last Seq plus one; an equal resend from the same Writer (slices by content,
// Time by Time.Equal) returns nil and writes nothing. Any other Seq of 1 or more is rejected, wrapping
// ErrExecutionExists at Seq 1 and ErrConcurrentWriter after it; a Seq below 1 is an invalid event with no sentinel.
type Journal interface {
	Append(ctx context.Context, e Event) error
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

// EventKind says what an Event records.
type EventKind int

// EventKind values; EventInvalid is the zero value and never a real event.
const (
	EventInvalid EventKind = iota
	EventSagaStarted
	EventStepStarted
	EventStepSucceeded
	EventStepFailed
	EventStepUnknown
	EventStepSkipped
	EventCompensationStarted
	EventCompensationSucceeded
	EventCompensationFailed
	EventCompensationBlocked
	EventCompensationHalted
	EventSagaFinished
)

// Zone says whether a step can be compensated: before the pivot, the pivot itself, or after it.
type Zone int

// Zone values; ZoneInvalid is the zero value and never a real zone.
const (
	ZoneInvalid Zone = iota
	ZoneCompensable
	ZonePivot
	ZoneForwardOnly
)

// String returns the event kind in words, or EventKind(n) for an unknown value.
func (k EventKind) String() string {
	names := [...]string{
		EventInvalid:               "invalid",
		EventSagaStarted:           "saga started",
		EventStepStarted:           "step started",
		EventStepSucceeded:         "step succeeded",
		EventStepFailed:            "step failed",
		EventStepUnknown:           "step unknown",
		EventStepSkipped:           "step skipped",
		EventCompensationStarted:   "compensation started",
		EventCompensationSucceeded: "compensation succeeded",
		EventCompensationFailed:    "compensation failed",
		EventCompensationBlocked:   "compensation blocked",
		EventCompensationHalted:    "compensation halted",
		EventSagaFinished:          "saga finished",
	}
	if k >= 0 && int(k) < len(names) {
		return names[k]
	}

	return "EventKind(" + strconv.Itoa(int(k)) + ")"
}

// String returns the zone in words, or Zone(n) for an unknown value.
func (z Zone) String() string {
	switch z {
	case ZoneInvalid:
		return "invalid"
	case ZoneCompensable:
		return "compensable"
	case ZonePivot:
		return "pivot"
	case ZoneForwardOnly:
		return "forward-only"
	}

	return "Zone(" + strconv.Itoa(int(z)) + ")"
}

type memoryJournal struct {
	mu      sync.Mutex
	streams map[string][]Event
}

func newMemoryJournal() *memoryJournal {
	return &memoryJournal{streams: map[string][]Event{}}
}

func (j *memoryJournal) Append(_ context.Context, e Event) error {
	if e.Seq < 1 {
		return fmt.Errorf("%w: execution %q seq %d", errInvalidSeq, e.Execution, e.Seq)
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	stream := j.streams[e.Execution]
	if e.Seq == len(stream)+1 {
		cloneSlices(&e)
		j.streams[e.Execution] = append(stream, e)

		return nil
	}

	if e.Seq <= len(stream) && sameEvent(&stream[e.Seq-1], &e) {
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
