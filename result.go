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

type stepStatus string

const (
	stepNotStarted         stepStatus = "not-started"
	stepSucceeded          stepStatus = "succeeded"
	stepFailed             stepStatus = "failed"
	stepUnknown            stepStatus = "unknown"
	stepSkipped            stepStatus = "skipped"
	stepCompensated        stepStatus = "compensated"
	stepCompensationFailed stepStatus = "compensation-failed"
	stepBlocked            stepStatus = "blocked"
	stepHalted             stepStatus = "halted"
)

type stepResult struct {
	Name            string
	Status          stepStatus
	Attempts        int
	Err             EventError
	CompensationErr EventError
	Changed         []string
	Start           time.Time
	End             time.Time
}

func pendingOf(events []Event) []string {
	var zones []StepZone

	for i := range events {
		if events[i].Kind == EventSagaStarted {
			zones = events[i].Steps
		}
	}

	steps := foldStepsSharingChanged(events)
	pivot := stepNotStarted

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

func owed(status stepStatus, zone Zone, pivot stepStatus) bool {
	switch status {
	case stepUnknown, stepCompensationFailed, stepBlocked, stepHalted:
		return true
	case stepFailed, stepNotStarted:
		return zone == ZoneForwardOnly && pivot == stepSucceeded
	case stepSucceeded, stepSkipped, stepCompensated:
		return false
	}

	return false
}

func foldStepsSharingChanged(events []Event) []stepResult {
	var steps []stepResult

	for i := range events {
		e := &events[i]

		if e.Kind == EventSagaStarted {
			steps = make([]stepResult, len(e.Steps))

			for k := range e.Steps {
				steps[k].Name, steps[k].Status = e.Steps[k].Name, stepNotStarted
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
		s.Status, s.Start = stepUnknown, e.Time
	case EventStepSucceeded, EventStepFailed, EventStepUnknown:
		s.Status, s.Attempts, s.Err, s.Changed, s.End = outcomeStatus(e.Kind), e.Attempt, e.Err, e.Changed, e.Time
	case EventStepSkipped:
		s.Status = stepSkipped
	case EventCompensationSucceeded, EventCompensationFailed, EventCompensationBlocked, EventCompensationHalted:
		s.Status, s.CompensationErr, s.End = outcomeStatus(e.Kind), e.Err, e.Time
	case EventCompensationStarted:
		s.Status = stepCompensationFailed
	case "", EventSagaStarted, EventSagaFinished:
		return
	default:
	}
}

func outcomeStatus(k EventKind) stepStatus {
	switch k {
	case EventStepSucceeded:
		return stepSucceeded
	case EventStepFailed:
		return stepFailed
	case EventStepUnknown:
		return stepUnknown
	case EventCompensationSucceeded:
		return stepCompensated
	case EventCompensationFailed:
		return stepCompensationFailed
	case EventCompensationBlocked:
		return stepBlocked
	case EventCompensationHalted:
		return stepHalted
	case "", EventSagaStarted, EventStepStarted, EventStepSkipped, EventCompensationStarted, EventSagaFinished:
		return ""
	default:
	}

	return ""
}
