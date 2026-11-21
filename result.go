package sagas

import (
	"slices"
	"time"
)

var (
	// ErrCompensated is returned when a run fails and every started compensable step is compensated.
	ErrCompensated = &KindError{name: "compensated", message: "sagas: compensated"}
	// ErrCompensationFailed is returned when a run fails and at least one compensation does not succeed.
	ErrCompensationFailed = &KindError{name: "compensation_failed", message: "sagas: compensation failed"}
	// ErrIncomplete is returned when a run cannot finish forward: the pivot is unknown or forward-only work is undone.
	ErrIncomplete = &KindError{name: "incomplete", message: "sagas: incomplete"}
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

type stepResult struct {
	Name            string
	Status          StepStatus
	Attempts        int
	Err             EventError
	CompensationErr EventError
	Changed         []string
	Start           time.Time
	End             time.Time
}

type result struct {
	Execution string
	Status    Status
	Steps     []stepResult
	events    []Event
}

func (r *result) pending() []string {
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

func buildResult(events []Event) result {
	r := result{Status: StatusUnfinished, events: copyEvents(events)}
	r.Steps = foldStepsSharingChanged(r.events)

	for k := range r.Steps {
		r.Steps[k].Changed = slices.Clone(r.Steps[k].Changed)
		r.Steps[k].Err.Previous = slices.Clone(r.Steps[k].Err.Previous)
		r.Steps[k].CompensationErr.Previous = slices.Clone(r.Steps[k].CompensationErr.Previous)
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

func foldStepsSharingChanged(events []Event) []stepResult {
	var steps []stepResult

	for i := range events {
		e := &events[i]

		if e.Kind == EventSagaStarted {
			steps = make([]stepResult, len(e.Steps))

			for k := range e.Steps {
				steps[k].Name, steps[k].Status = e.Steps[k].Name, StepNotStarted
			}
		}

		if k := slices.IndexFunc(steps, func(s stepResult) bool { return s.Name == e.Step }); k >= 0 {
			applyEventSharingChanged(&steps[k], e)
		}
	}

	return steps
}

func applyEventSharingChanged(s *stepResult, e *Event) {
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
