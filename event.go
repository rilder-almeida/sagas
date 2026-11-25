package sagas

import (
	"encoding/json/jsontext"
	"strconv"
	"strings"
	"time"
)

// EventVersion is the semantic version of the writing package, as vMAJOR.MINOR.PATCH with no pre-release. A version
// of that form is compatible with it when it has the same 0.minor before v1.0.0, and the same major from v1.0.0 on.
const EventVersion = "v0.2.0"

// Event is one journal record of an execution. Writer is the token of the run that wrote it, and Seq counts from 1.
// Version is the EventVersion of the writing package. Step names the step of a step or compensation event. Attempts
// counts the attempts made, and zero means the call never ran. Err is set on a failed, unknown or compensation-failed
// event. Values holds every state field on saga-started and the output of a success. Steps lists every step on
// saga-started. Status is the end of the run on saga-finished and saga-suspended.
type Event struct {
	Saga      string
	Execution string
	Writer    string
	Seq       int
	Version   string
	Kind      EventKind
	Step      string
	Attempts  int
	Err       EventError
	Values    []FieldValue
	Steps     []StepZone
	Status    Status
	Time      time.Time
}

// FieldValue is one state field by its Go name. Value holds its JSON, null included, and is empty for a secret field.
type FieldValue struct {
	Field string
	Value jsontext.Value
}

// EventError is an Error as text. Kind is the name of the error kind. Message and Cause are the texts of Error.Err and
// Error.Cause, and Previous holds the texts of the earlier errors. The zero value means no error.
type EventError struct {
	Kind     string
	Message  string
	Cause    string
	Previous []string
}

// StepZone is one entry of Event.Steps. Compensable is true when the step has a compensation.
type StepZone struct {
	Name        string
	Zone        Zone
	Compensable bool
}

// EventKind says what an Event records. The empty string means not set and is never a real event.
type EventKind string

// saga-finished ends a completed or compensated run. saga-suspended ends any other run, which waits for repair.
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
	EventSagaFinished          EventKind = "saga-finished"
	EventSagaSuspended         EventKind = "saga-suspended"
)

// Zone says where a step runs: before the pivot, as the pivot or after it. The empty string is unset, never a zone.
type Zone string

// Zone values.
const (
	ZoneCompensable Zone = "compensable"
	ZonePivot       Zone = "pivot"
	ZoneForwardOnly Zone = "forward-only"
)

func compatibleVersion(v string) bool {
	got, ok := parseVersion(v)
	if !ok {
		return false
	}

	want, _ := parseVersion(EventVersion)
	if want[0] == 0 {
		return got[0] == 0 && got[1] == want[1]
	}

	return got[0] == want[0]
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int

	rest, found := strings.CutPrefix(v, "v")
	parts := strings.Split(rest, ".")

	if !found || len(parts) != len(out) {
		return out, false
	}

	for i, part := range parts {
		if strings.TrimLeft(part, "0123456789") != "" || len(part) > 1 && part[0] == '0' {
			return out, false
		}

		n, err := strconv.Atoi(part)
		if err != nil {
			return out, false
		}

		out[i] = n
	}

	return out, true
}
