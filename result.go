package sagas

import (
	"errors"
	"slices"
	"strconv"
	"time"
)

var (
	// ErrCompensated is returned when a run fails and every completed compensable step is compensated.
	ErrCompensated = errors.New("sagas: compensated")
	// ErrCompensationFailed is returned when a run fails and at least one compensation does not succeed.
	ErrCompensationFailed = errors.New("sagas: compensation failed")
	// ErrIncomplete is returned when a run stops after its pivot with forward-only work still pending.
	ErrIncomplete = errors.New("sagas: incomplete")
)

// Status is the outcome of a run.
type Status int

// Status values; StatusUnfinished is the zero value, for a run with no saga-finished event.
const (
	StatusUnfinished Status = iota
	StatusCompleted
	StatusCompensated
	StatusCompensationFailed
	StatusIncomplete
)

// StepStatus is where one step of a run ended.
type StepStatus int

// StepStatus values; StepNotStarted is the zero value.
const (
	StepNotStarted StepStatus = iota
	StepSucceeded
	StepFailed
	StepUnknown
	StepSkipped
	StepCompensated
	StepCompensationFailed
	StepBlocked
	StepHalted
)

// String returns the status in words, or Status(n) for an unknown value.
func (s Status) String() string {
	switch s {
	case StatusUnfinished:
		return "unfinished"
	case StatusCompleted:
		return "completed"
	case StatusCompensated:
		return "compensated"
	case StatusCompensationFailed:
		return "compensation failed"
	case StatusIncomplete:
		return "incomplete"
	}

	return "Status(" + strconv.Itoa(int(s)) + ")"
}

// String returns the step status in words, or StepStatus(n) for an unknown value.
func (s StepStatus) String() string {
	switch s {
	case StepNotStarted:
		return "not started"
	case StepSucceeded:
		return "succeeded"
	case StepFailed:
		return "failed"
	case StepUnknown:
		return "unknown"
	case StepSkipped:
		return "skipped"
	case StepCompensated:
		return "compensated"
	case StepCompensationFailed:
		return "compensation failed"
	case StepBlocked:
		return "blocked"
	case StepHalted:
		return "halted"
	}

	return "StepStatus(" + strconv.Itoa(int(s)) + ")"
}

// StepResult is where one step of a run ended, with its attempts, errors, committed fields and times.
type StepResult struct {
	Name            string
	Status          StepStatus
	Attempts        int
	Err             string
	CompensationErr string
	Changed         []string
	Start           time.Time
	End             time.Time
}

// Result is the outcome of one execution, built from its journal events.
type Result struct {
	Execution string
	Status    Status
	Steps     []StepResult
	events    []Event
}

// Pending lists, in step order and from the events alone, every step that needs action: an unknown step; a failed,
// blocked or halted compensation; and, once the pivot succeeded, a forward-only step that failed or did not start.
// During a journal outage, the error Run returns is the complete list: the journal holds only what it recorded.
func (r Result) Pending() []string {
	var zones []StepZone

	for i := range r.events {
		if r.events[i].Kind == EventSagaStarted {
			zones = r.events[i].Steps
		}
	}

	steps := foldStepsSharingChanged(r.events)
	pivot := StepNotStarted

	for i, z := range zones {
		if z.Zone == ZonePivot {
			pivot = steps[i].Status
		}
	}

	var pending []string

	for i, z := range zones {
		if owed(steps[i].Status, z.Zone, pivot) {
			pending = append(pending, steps[i].Name)
		}
	}

	return pending
}

// Events returns a copy of the run's events that the caller may change freely.
func (r Result) Events() []Event {
	return copyEvents(r.events)
}

func owed(status StepStatus, zone Zone, pivot StepStatus) bool {
	switch status {
	case StepUnknown, StepCompensationFailed, StepBlocked, StepHalted:
		return true
	case StepFailed, StepNotStarted:
		return zone == ZoneForwardOnly && pivot == StepSucceeded
	case StepSucceeded, StepSkipped, StepCompensated:
		return false
	}

	return false
}

func buildResult(events []Event) Result {
	r := Result{events: copyEvents(events)}
	r.Steps = foldStepsSharingChanged(r.events)

	for k := range r.Steps {
		r.Steps[k].Changed = slices.Clone(r.Steps[k].Changed)
	}

	for i := range r.events {
		if r.events[i].Kind == EventSagaStarted {
			r.Execution = r.events[i].Execution
		}

		if r.events[i].Kind == EventSagaFinished {
			r.Status = r.events[i].Status
		}
	}

	return r
}

func foldStepsSharingChanged(events []Event) []StepResult {
	var steps []StepResult

	for i := range events {
		e := &events[i]

		if e.Kind == EventSagaStarted {
			steps = make([]StepResult, len(e.Steps))

			for k := range e.Steps {
				steps[k].Name = e.Steps[k].Name
			}
		}

		if k := slices.IndexFunc(steps, func(s StepResult) bool { return s.Name == e.Step }); k >= 0 {
			applyEventSharingChanged(&steps[k], e)
		}
	}

	return steps
}

func applyEventSharingChanged(s *StepResult, e *Event) {
	statuses := [...]StepStatus{
		EventStepSucceeded:         StepSucceeded,
		EventStepFailed:            StepFailed,
		EventStepUnknown:           StepUnknown,
		EventStepSkipped:           StepSkipped,
		EventCompensationSucceeded: StepCompensated,
		EventCompensationFailed:    StepCompensationFailed,
		EventCompensationBlocked:   StepBlocked,
		EventCompensationHalted:    StepHalted,
	}

	switch e.Kind {
	case EventStepStarted:
		s.Status, s.Start = StepUnknown, e.Time

		return
	case EventStepSucceeded, EventStepFailed, EventStepUnknown:
		s.Status, s.Attempts, s.Err, s.Changed, s.End = statuses[e.Kind], e.Attempt, e.Err, e.Changed, e.Time

		return
	case EventStepSkipped:
		s.Status = StepSkipped

		return
	case EventCompensationSucceeded, EventCompensationFailed, EventCompensationBlocked, EventCompensationHalted:
		s.Status, s.CompensationErr, s.End = statuses[e.Kind], e.Err, e.Time

		return
	case EventCompensationStarted:
		s.Status = StepCompensationFailed

		return
	case EventInvalid, EventSagaStarted, EventSagaFinished:
		return
	}
}
