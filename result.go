package sagas

import (
	"errors"
	"slices"
	"time"
)

var (
	// ErrCompensated is returned when a run fails and every started compensable step is compensated.
	ErrCompensated = errors.New("sagas: compensated")
	// ErrCompensationFailed is returned when a run fails and at least one compensation does not succeed.
	ErrCompensationFailed = errors.New("sagas: compensation failed")
	// ErrIncomplete is returned when a run cannot finish forward: the pivot's outcome stays unknown, or
	// forward-only work is left undone after the pivot succeeds.
	ErrIncomplete = errors.New("sagas: incomplete")
)

// Status is the outcome of a run. The empty string means not set.
type Status string

// Status values.
const (
	StatusUnfinished         Status = "unfinished"
	StatusCompleted          Status = "completed"
	StatusCompensated        Status = "compensated"
	StatusCompensationFailed Status = "compensation-failed"
	StatusIncomplete         Status = "incomplete"
)

// StepStatus is where one step of a run ended. The empty string means not set.
type StepStatus string

// StepStatus values.
const (
	StepNotStarted         StepStatus = "not-started"
	StepSucceeded          StepStatus = "succeeded"
	StepFailed             StepStatus = "failed"
	StepUnknown            StepStatus = "unknown"
	StepSkipped            StepStatus = "skipped"
	StepCompensated        StepStatus = "compensated"
	StepCompensationFailed StepStatus = "compensation-failed"
	StepBlocked            StepStatus = "blocked"
	StepHalted             StepStatus = "halted"
)

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

// Result is the outcome of one execution, built from its journal events. It describes a run that ended: a step
// still running looks unknown, and a compensation still running looks failed.
type Result struct {
	Execution string
	Status    Status
	Steps     []StepResult
	events    []Event
}

// Pending lists, in step order and from the events alone, every step that needs action: an unknown step; a failed,
// blocked or halted compensation; and, once the pivot succeeded, a forward-only step that failed or did not start.
// During a journal outage, the error Run returns is the complete list: the journal holds only what it recorded.
func (r *Result) Pending() []string {
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
func (r *Result) Events() []Event {
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
	r := Result{Status: StatusUnfinished, events: copyEvents(events)}
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
				steps[k].Name, steps[k].Status = e.Steps[k].Name, StepNotStarted
			}
		}

		if k := slices.IndexFunc(steps, func(s StepResult) bool { return s.Name == e.Step }); k >= 0 {
			applyEventSharingChanged(&steps[k], e)
		}
	}

	return steps
}

func applyEventSharingChanged(s *StepResult, e *Event) {
	switch e.Kind {
	case EventStepStarted:
		s.Status, s.Start = StepUnknown, e.Time
	case EventStepSucceeded, EventStepFailed, EventStepUnknown:
		s.Status, s.Attempts, s.Err, s.Changed, s.End = outcomeStatus(e.Kind), e.Attempt, e.Err, e.Changed, e.Time
	case EventStepSkipped:
		s.Status = StepSkipped
	case EventCompensationSucceeded, EventCompensationFailed, EventCompensationBlocked, EventCompensationHalted:
		s.Status, s.CompensationErr, s.End = outcomeStatus(e.Kind), e.Err, e.Time
	case EventCompensationStarted:
		s.Status = StepCompensationFailed
	case "", EventSagaStarted, EventSagaFinished:
		return
	default:
	}
}

func outcomeStatus(k EventKind) StepStatus {
	switch k {
	case EventStepSucceeded:
		return StepSucceeded
	case EventStepFailed:
		return StepFailed
	case EventStepUnknown:
		return StepUnknown
	case EventCompensationSucceeded:
		return StepCompensated
	case EventCompensationFailed:
		return StepCompensationFailed
	case EventCompensationBlocked:
		return StepBlocked
	case EventCompensationHalted:
		return StepHalted
	case "", EventSagaStarted, EventStepStarted, EventStepSkipped, EventCompensationStarted, EventSagaFinished:
		return ""
	default:
	}

	return ""
}
