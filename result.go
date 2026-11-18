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

// Pending lists, in step order, every step that still needs action: a compensation that failed, was blocked or was
// halted; an unknown pivot; and, only once the pivot succeeded, a forward-only step that failed, ended unknown or did
// not start.
func (r Result) Pending() []string {
	var zones []StepZone

	for i := range r.events {
		if r.events[i].Kind == EventSagaStarted {
			zones = r.events[i].Steps

			break
		}
	}

	zones = zones[:min(len(zones), len(r.Steps))]
	pivot := StepNotStarted

	for i, z := range zones {
		if z.Zone == ZonePivot {
			pivot = r.Steps[i].Status
		}
	}

	var pending []string

	for i, z := range zones {
		if owed(r.Steps[i].Status, z.Zone, pivot) {
			pending = append(pending, r.Steps[i].Name)
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
	case StepCompensationFailed, StepBlocked, StepHalted:
		return true
	case StepUnknown:
		return zone == ZonePivot || zone == ZoneForwardOnly && pivot == StepSucceeded
	case StepFailed, StepNotStarted:
		return zone == ZoneForwardOnly && pivot == StepSucceeded
	case StepSucceeded, StepSkipped, StepCompensated:
		return false
	}

	return false
}

func buildResult(events []Event) Result {
	r := Result{events: copyEvents(events)}

	for i := range r.events {
		e := &r.events[i]

		if e.Kind == EventSagaStarted {
			r.Execution = e.Execution
			r.Steps = make([]StepResult, len(e.Steps))

			for k := range e.Steps {
				r.Steps[k].Name = e.Steps[k].Name
			}
		}

		if e.Kind == EventSagaFinished {
			r.Status = e.Status
		}

		if k := slices.IndexFunc(r.Steps, func(s StepResult) bool { return s.Name == e.Step }); k >= 0 {
			applyEvent(&r.Steps[k], e)
		}
	}

	return r
}

func applyEvent(s *StepResult, e *Event) {
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
		s.Start = e.Time

		return
	case EventStepSucceeded, EventStepFailed, EventStepUnknown:
		s.Status, s.Attempts, s.Err, s.End = statuses[e.Kind], e.Attempt, e.Err, e.Time
		s.Changed = slices.Clone(e.Changed)

		return
	case EventStepSkipped:
		s.Status = StepSkipped

		return
	case EventCompensationSucceeded, EventCompensationFailed, EventCompensationBlocked, EventCompensationHalted:
		s.Status, s.CompensationErr, s.End = statuses[e.Kind], e.Err, e.Time

		return
	case EventInvalid, EventSagaStarted, EventCompensationStarted, EventSagaFinished:
		return
	}
}
