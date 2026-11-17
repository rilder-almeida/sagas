package sagas

import (
	"errors"
	"strconv"
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
