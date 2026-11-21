package sagas

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const (
	holdStep    = "hold"
	bookStep    = "book"
	payStep     = "pay"
	shipStep    = "ship"
	notifyStep  = "notify"
	archiveStep = "archive"
)

func runSteps() []StepZone {
	return []StepZone{
		{Name: holdStep, Zone: ZoneCompensable},
		{Name: bookStep, Zone: ZoneCompensable},
		{Name: payStep, Zone: ZonePivot},
		{Name: shipStep, Zone: ZoneForwardOnly},
		{Name: notifyStep, Zone: ZoneForwardOnly},
		{Name: archiveStep, Zone: ZoneForwardOnly},
	}
}

func eventTime(seq int) time.Time {
	return journalEvent("", seq).Time
}

func runEvents(tail ...Event) []Event {
	return runEventsFrom(append([]Event{{Kind: EventSagaStarted, Steps: runSteps()}}, tail...))
}

func runEventsFrom(events []Event) []Event {
	for i := range events {
		base := journalEvent("x1", i+1)
		events[i].Saga, events[i].Execution, events[i].Writer = base.Saga, base.Execution, base.Writer
		events[i].Seq, events[i].Time = base.Seq, base.Time
	}

	return events
}

func wantSteps(reached ...stepResult) []stepResult {
	zones := runSteps()
	steps := make([]stepResult, len(zones))

	for i := range zones {
		steps[i].Name, steps[i].Status = zones[i].Name, "not-started"
	}

	for i := range reached {
		k := slices.IndexFunc(steps, func(s stepResult) bool { return s.Name == reached[i].Name })
		steps[k] = reached[i]
	}

	return steps
}

func literalEvents(tail ...Event) []Event {
	steps := []StepZone{
		{Name: holdStep, Zone: "compensable"},
		{Name: bookStep, Zone: "compensable"},
		{Name: payStep, Zone: "pivot"},
		{Name: shipStep, Zone: "forward-only"},
		{Name: notifyStep, Zone: "forward-only"},
		{Name: archiveStep, Zone: "forward-only"},
	}

	return runEventsFrom(append([]Event{{Kind: "saga-started", Steps: steps}}, tail...))
}

type foldRow struct {
	name   string
	events []Event
	want   []stepResult
}

func finished(status Status) Event {
	return Event{Kind: "saga-finished", Status: status}
}

func storedTextRows() []foldRow {
	const compensationFailed = "compensation-failed"

	return []foldRow{
		{
			name: "[Success] When: the stored texts say a run completed ; Should: report the step succeeded",
			events: literalEvents(
				Event{Kind: "step-started", Step: holdStep},
				Event{Kind: "step-succeeded", Step: holdStep},
				finished(StatusCompleted),
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: "succeeded", Start: eventTime(2), End: eventTime(3)},
			),
		},
		{
			name: "[Success] When: the stored texts say a run was compensated ; Should: report compensated and failed",
			events: literalEvents(
				Event{Kind: "step-succeeded", Step: holdStep},
				Event{Kind: "step-failed", Step: bookStep},
				Event{Kind: "compensation-started", Step: holdStep},
				Event{Kind: "compensation-succeeded", Step: holdStep},
				finished(StatusCompensated),
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: "compensated", End: eventTime(5)},
				stepResult{Name: bookStep, Status: "failed", End: eventTime(3)},
			),
		},
		{
			name: "[Fail] When: the stored texts say a compensation failed ; Should: report it and halted",
			events: literalEvents(
				Event{Kind: "compensation-failed", Step: bookStep},
				Event{Kind: "compensation-halted", Step: holdStep},
				finished(StatusCompensationFailed),
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: "halted", End: eventTime(3)},
				stepResult{Name: bookStep, Status: compensationFailed, End: eventTime(2)},
			),
		},
		{
			name: "[Fail] When: a stored compensation starts last ; Should: report the step compensation-failed",
			events: literalEvents(
				Event{Kind: "compensation-started", Step: holdStep},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: compensationFailed},
			),
		},
		{
			name: "[Fail] When: the stored texts say a run is incomplete ; Should: report unknown, skipped and blocked",
			events: literalEvents(
				Event{Kind: "step-unknown", Step: payStep},
				Event{Kind: "step-skipped", Step: shipStep},
				Event{Kind: "compensation-blocked", Step: bookStep},
				finished(StatusIncomplete),
			),
			want: wantSteps(
				stepResult{Name: bookStep, Status: "blocked", End: eventTime(4)},
				stepResult{Name: payStep, Status: "unknown", End: eventTime(2)},
				stepResult{Name: shipStep, Status: "skipped"},
			),
		},
		{
			name:   "[Fail] When: the stored texts hold only saga-started ; Should: report not-started",
			events: literalEvents(),
			want:   wantSteps(),
		},
	}
}

func Test_foldStepsSharingChanged(t *testing.T) {
	t.Parallel()

	tests := append([]foldRow{
		{
			name: "[Success] When: every step succeeds ; Should: report each step outcome",
			events: runEvents(
				Event{Kind: EventStepStarted, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: holdStep, Attempt: 1, Changed: []string{itemsField}},
				Event{Kind: EventStepStarted, Step: bookStep},
				Event{Kind: EventStepSucceeded, Step: bookStep, Attempt: 2, Changed: []string{seatsField}},
				Event{Kind: EventStepSucceeded, Step: payStep, Attempt: 1},
				Event{Kind: EventStepSucceeded, Step: shipStep, Attempt: 1},
				Event{Kind: EventStepSucceeded, Step: notifyStep, Attempt: 1},
				Event{Kind: EventStepSucceeded, Step: archiveStep, Attempt: 3},
				Event{Kind: EventSagaFinished, Status: StatusCompleted},
			),
			want: wantSteps(
				stepResult{
					Name: holdStep, Status: stepSucceeded, Attempts: 1, Changed: []string{itemsField},
					Start: eventTime(2), End: eventTime(3),
				},
				stepResult{
					Name: bookStep, Status: stepSucceeded, Attempts: 2, Changed: []string{seatsField},
					Start: eventTime(4), End: eventTime(5),
				},
				stepResult{Name: payStep, Status: stepSucceeded, Attempts: 1, End: eventTime(6)},
				stepResult{Name: shipStep, Status: stepSucceeded, Attempts: 1, End: eventTime(7)},
				stepResult{Name: notifyStep, Status: stepSucceeded, Attempts: 1, End: eventTime(8)},
				stepResult{Name: archiveStep, Status: stepSucceeded, Attempts: 3, End: eventTime(9)},
			),
		},
		{
			name: "[Success] When: steps are skipped ; Should: mark them skipped",
			events: runEvents(
				Event{Kind: EventStepSkipped, Step: holdStep},
				Event{Kind: EventStepSkipped, Step: bookStep},
				Event{Kind: EventSagaFinished, Status: StatusCompleted},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepSkipped},
				stepResult{Name: bookStep, Status: stepSkipped},
			),
		},
		{
			name: "[Success] When: a step fails and an earlier one is compensated ; Should: end it at its compensation",
			events: runEvents(
				Event{Kind: EventStepStarted, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: holdStep, Attempt: 1, Changed: []string{itemsField}},
				Event{Kind: EventStepFailed, Step: bookStep, Attempt: 3, Err: EventError{Message: "sold out"}},
				Event{Kind: EventCompensationStarted, Step: holdStep},
				Event{Kind: EventCompensationSucceeded, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensated},
			),
			want: wantSteps(
				stepResult{
					Name: holdStep, Status: stepCompensated, Attempts: 1, Changed: []string{itemsField},
					Start: eventTime(2), End: eventTime(6),
				},
				stepResult{
					Name:     bookStep,
					Status:   stepFailed,
					Attempts: 3,
					Err:      EventError{Message: "sold out"},
					End:      eventTime(4),
				},
			),
		},
		{
			name: "[Success] When: a step ends unknown and is compensated ; Should: report it compensated",
			events: runEvents(
				Event{Kind: EventStepUnknown, Step: holdStep},
				Event{Kind: EventCompensationSucceeded, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensated},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepCompensated, End: eventTime(3)},
			),
		},
		{
			name: "[Fail] When: a compensation fails ; Should: report it with its compensation error",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepFailed, Step: bookStep, Err: EventError{Message: "declined"}},
				Event{Kind: EventCompensationFailed, Step: holdStep, Err: EventError{Message: "refund down"}},
				Event{Kind: EventSagaFinished, Status: StatusCompensationFailed},
			),
			want: wantSteps(
				stepResult{
					Name:            holdStep,
					Status:          stepCompensationFailed,
					CompensationErr: EventError{Message: "refund down"},
					End:             eventTime(4),
				},
				stepResult{Name: bookStep, Status: stepFailed, Err: EventError{Message: "declined"}, End: eventTime(3)},
			),
		},
		{
			name: "[Fail] When: a compensation is blocked ; Should: report it blocked",
			events: runEvents(
				Event{Kind: EventCompensationFailed, Step: bookStep},
				Event{Kind: EventCompensationBlocked, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensationFailed},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepBlocked, End: eventTime(3)},
				stepResult{Name: bookStep, Status: stepCompensationFailed, End: eventTime(2)},
			),
		},
		{
			name: "[Fail] When: a compensation is halted ; Should: report it halted",
			events: runEvents(
				Event{Kind: EventCompensationFailed, Step: bookStep},
				Event{Kind: EventCompensationHalted, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensationFailed},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepHalted, End: eventTime(3)},
				stepResult{Name: bookStep, Status: stepCompensationFailed, End: eventTime(2)},
			),
		},
		{
			name:   "[Success] When: only the saga-started event exists ; Should: list every step not started",
			events: runEvents(),
			want:   wantSteps(),
		},
		{
			name: "[Fail] When: the pivot ends unknown ; Should: report it unknown",
			events: runEvents(
				Event{Kind: EventStepUnknown, Step: payStep, Attempt: 4},
				Event{Kind: EventSagaFinished, Status: StatusIncomplete},
			),
			want: wantSteps(
				stepResult{Name: payStep, Status: stepUnknown, Attempts: 4, End: eventTime(2)},
			),
		},
		{
			name: "[Fail] When: forward-only steps fail, end unknown or never start ; Should: report each one",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: payStep},
				Event{Kind: EventStepFailed, Step: shipStep},
				Event{Kind: EventStepUnknown, Step: notifyStep},
				Event{Kind: EventSagaFinished, Status: StatusIncomplete},
			),
			want: wantSteps(
				stepResult{Name: payStep, Status: stepSucceeded, End: eventTime(2)},
				stepResult{Name: shipStep, Status: stepFailed, End: eventTime(3)},
				stepResult{Name: notifyStep, Status: stepUnknown, End: eventTime(4)},
			),
		},
		{
			name: "[Fail] When: the stream has no saga-finished event ; Should: report the steps it reached",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepSucceeded, End: eventTime(2)},
				stepResult{Name: bookStep, Status: stepSucceeded, End: eventTime(3)},
			),
		},
		{
			name: "[Fail] When: the pivot starts and its outcome is lost ; Should: report it unknown",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepStarted, Step: payStep},
			),
			want: wantSteps(
				stepResult{Name: bookStep, Status: stepSucceeded, End: eventTime(2)},
				stepResult{Name: payStep, Status: stepUnknown, Start: eventTime(3)},
			),
		},
		{
			name: "[Fail] When: a compensation starts and its outcome is lost ; Should: report it failed",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepFailed, Step: bookStep},
				Event{Kind: EventCompensationStarted, Step: holdStep},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepCompensationFailed, End: eventTime(2)},
				stepResult{Name: bookStep, Status: stepFailed, End: eventTime(3)},
			),
		},
		{
			name: "[Fail] When: an event names no listed step ; Should: ignore it",
			events: runEvents(
				Event{Kind: EventStepFailed, Step: "audit", Attempt: 1},
			),
			want: wantSteps(),
		},
		{
			name:   "[Fail] When: the stream is empty ; Should: return no steps",
			events: nil,
			want:   nil,
		},
	}, storedTextRows()...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, foldStepsSharingChanged(tt.events))
		})
	}
}

func Test_applyEventSharingChanged(t *testing.T) {
	t.Parallel()

	prior := func() stepResult {
		return stepResult{
			Name:            holdStep,
			Status:          stepSucceeded,
			Attempts:        2,
			Err:             EventError{Message: "late"},
			CompensationErr: EventError{Message: "refund late"},
			Changed:         []string{seatsField},
			Start:           eventTime(1),
			End:             eventTime(2),
		}
	}
	with := func(change func(*stepResult)) stepResult {
		s := prior()
		change(&s)

		return s
	}

	tests := []struct {
		name  string
		event Event
		want  stepResult
	}{
		{
			name:  "[Success] When: the step starts ; Should: set Start and the status unknown until an outcome",
			event: Event{Kind: EventStepStarted, Attempt: 9, Err: EventError{Message: "x"}, Time: eventTime(9)},
			want:  with(func(s *stepResult) { s.Status, s.Start = stepUnknown, eventTime(9) }),
		},
		{
			name: "[Success] When: the step succeeds ; Should: set status, attempts, error, changed fields and End",
			event: Event{
				Kind: EventStepSucceeded, Attempt: 3, Changed: []string{itemsField, chargeField}, Time: eventTime(9),
			},
			want: with(func(s *stepResult) {
				s.Status, s.Attempts, s.Err, s.End = stepSucceeded, 3, EventError{}, eventTime(9)
				s.Changed = []string{itemsField, chargeField}
			}),
		},
		{
			name:  "[Success] When: the step fails ; Should: set status, attempts, error, no changed fields and End",
			event: Event{Kind: EventStepFailed, Attempt: 4, Err: EventError{Message: "no stock"}, Time: eventTime(9)},
			want: with(func(s *stepResult) {
				s.Status, s.Attempts, s.Err, s.Changed, s.End = stepFailed, 4, EventError{
					Message: "no stock",
				}, nil, eventTime(
					9,
				)
			}),
		},
		{
			name:  "[Success] When: the step ends unknown ; Should: set status, attempts, error and End",
			event: Event{Kind: EventStepUnknown, Attempt: 1, Err: EventError{Message: "timeout"}, Time: eventTime(9)},
			want: with(func(s *stepResult) {
				s.Status, s.Attempts, s.Err, s.Changed, s.End = stepUnknown, 1, EventError{
					Message: "timeout",
				}, nil, eventTime(
					9,
				)
			}),
		},
		{
			name:  "[Success] When: the step is skipped ; Should: set only the status",
			event: Event{Kind: EventStepSkipped, Attempt: 9, Err: EventError{Message: "x"}, Time: eventTime(9)},
			want:  with(func(s *stepResult) { s.Status = stepSkipped }),
		},
		{
			name:  "[Success] When: the compensation succeeds ; Should: set status, compensation error and End",
			event: Event{Kind: EventCompensationSucceeded, Attempt: 9, Time: eventTime(9)},
			want: with(
				func(s *stepResult) {
					s.Status, s.CompensationErr, s.End = stepCompensated, EventError{}, eventTime(9)
				},
			),
		},
		{
			name:  "[Success] When: the compensation fails ; Should: set status, compensation error and End",
			event: Event{Kind: EventCompensationFailed, Err: EventError{Message: "refund timeout"}, Time: eventTime(9)},
			want: with(func(s *stepResult) {
				s.Status, s.CompensationErr, s.End = stepCompensationFailed, EventError{
					Message: "refund timeout",
				}, eventTime(
					9,
				)
			}),
		},
		{
			name:  "[Success] When: the compensation is blocked ; Should: set status, compensation error and End",
			event: Event{Kind: EventCompensationBlocked, Err: EventError{Message: "waits on book"}, Time: eventTime(9)},
			want: with(
				func(s *stepResult) {
					s.Status, s.CompensationErr, s.End = stepBlocked, EventError{Message: "waits on book"}, eventTime(9)
				},
			),
		},
		{
			name:  "[Success] When: the compensation is halted ; Should: set status, compensation error and End",
			event: Event{Kind: EventCompensationHalted, Time: eventTime(9)},
			want: with(
				func(s *stepResult) { s.Status, s.CompensationErr, s.End = stepHalted, EventError{}, eventTime(9) },
			),
		},
		{
			name:  "[Success] When: the compensation starts ; Should: set only the status failed until an outcome",
			event: Event{Kind: EventCompensationStarted, Attempt: 9, Err: EventError{Message: "x"}, Time: eventTime(9)},
			want:  with(func(s *stepResult) { s.Status = stepCompensationFailed }),
		},
		{
			name: "[Fail] When: the event is a saga event ; Should: change nothing",
			event: Event{
				Kind:   EventSagaFinished,
				Status: StatusCompleted,
				Err:    EventError{Message: "x"},
				Time:   eventTime(9),
			},
			want: prior(),
		},
		{
			name:  "[Fail] When: the kind is not set ; Should: change nothing",
			event: Event{Kind: "", Attempt: 9, Err: EventError{Message: "x"}, Time: eventTime(9)},
			want:  prior(),
		},
		{
			name: "[Fail] When: the kind is unknown ; Should: change nothing",
			event: Event{
				Kind:    EventKind("no such kind"),
				Attempt: 9,
				Err:     EventError{Message: "x"},
				Time:    eventTime(9),
			},
			want: prior(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := prior()
			applyEventSharingChanged(&got, &tt.event)

			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_pendingOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		events []Event
		want   []string
	}{
		{
			name: "[Success] When: every step succeeds ; Should: list nothing",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepSucceeded, Step: payStep},
				Event{Kind: EventStepSucceeded, Step: shipStep},
				Event{Kind: EventStepSucceeded, Step: notifyStep},
				Event{Kind: EventStepSucceeded, Step: archiveStep},
				Event{Kind: EventSagaFinished, Status: StatusCompleted},
			),
			want: nil,
		},
		{
			name: "[Success] When: a step fails before the pivot and is compensated ; Should: list nothing",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepFailed, Step: bookStep},
				Event{Kind: EventCompensationSucceeded, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensated},
			),
			want: nil,
		},
		{
			name: "[Success] When: the pivot fails and the run is compensated ; Should: list no forward-only step",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepFailed, Step: payStep},
				Event{Kind: EventCompensationSucceeded, Step: bookStep},
				Event{Kind: EventCompensationSucceeded, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensated},
			),
			want: nil,
		},
		{
			name: "[Fail] When: the stored zones say the pivot succeeded ; Should: list forward-only steps left undone",
			events: literalEvents(
				Event{Kind: "step-failed", Step: holdStep},
				Event{Kind: "step-succeeded", Step: payStep},
				Event{Kind: "step-failed", Step: shipStep},
			),
			want: []string{shipStep, notifyStep, archiveStep},
		},
		{
			name: "[Fail] When: a compensation fails and another is blocked ; Should: list both in step order",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepFailed, Step: payStep},
				Event{Kind: EventCompensationFailed, Step: bookStep},
				Event{Kind: EventCompensationBlocked, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensationFailed},
			),
			want: []string{holdStep, bookStep},
		},
		{
			name: "[Fail] When: a compensation fails and another is halted ; Should: list both in step order",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepFailed, Step: payStep},
				Event{Kind: EventCompensationFailed, Step: bookStep},
				Event{Kind: EventCompensationHalted, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensationFailed},
			),
			want: []string{holdStep, bookStep},
		},
		{
			name: "[Fail] When: the pivot ends unknown ; Should: list only the pivot",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepUnknown, Step: payStep},
				Event{Kind: EventSagaFinished, Status: StatusIncomplete},
			),
			want: []string{payStep},
		},
		{
			name: "[Fail] When: after the pivot forward-only steps fail, end unknown, never start ; Should: list them",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepSucceeded, Step: payStep},
				Event{Kind: EventStepFailed, Step: shipStep},
				Event{Kind: EventStepUnknown, Step: notifyStep},
				Event{Kind: EventSagaFinished, Status: StatusIncomplete},
			),
			want: []string{shipStep, notifyStep, archiveStep},
		},
		{
			name: "[Fail] When: after the pivot one forward-only step succeeds ; Should: list the others",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSkipped, Step: bookStep},
				Event{Kind: EventStepSucceeded, Step: payStep},
				Event{Kind: EventStepSucceeded, Step: shipStep},
				Event{Kind: EventStepFailed, Step: notifyStep},
				Event{Kind: EventSagaFinished, Status: StatusIncomplete},
			),
			want: []string{notifyStep, archiveStep},
		},
		{
			name: "[Fail] When: the pivot starts and its outcome is lost ; Should: list only the pivot",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepStarted, Step: payStep},
			),
			want: []string{payStep},
		},
		{
			name: "[Fail] When: a compensable step loses its outcome and is never compensated ; Should: list it",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepStarted, Step: bookStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensationFailed},
			),
			want: []string{bookStep},
		},
		{
			name: "[Success] When: a compensable step ends unknown and is compensated ; Should: list nothing",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepUnknown, Step: bookStep},
				Event{Kind: EventCompensationSucceeded, Step: bookStep},
				Event{Kind: EventCompensationSucceeded, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensated},
			),
			want: nil,
		},
		{
			name: "[Fail] When: a compensation starts and its outcome is lost ; Should: list its step",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepFailed, Step: bookStep},
				Event{Kind: EventCompensationStarted, Step: holdStep},
			),
			want: []string{holdStep},
		},
		{
			name:   "[Success] When: the stream is empty ; Should: list nothing",
			events: nil,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, pendingOf(tt.events))
		})
	}
}

func benchEvents(steps int) []Event {
	started := Event{Kind: EventSagaStarted, Execution: "x1", Seq: 1, Steps: make([]StepZone, steps)}
	events := make([]Event, 1, 2*steps+2)
	events[0] = started

	for i := range steps {
		name := "step" + strconv.Itoa(i)
		started.Steps[i] = StepZone{Name: name, Zone: ZoneCompensable}
		events = append(
			events,
			Event{Kind: EventStepStarted, Step: name, Seq: len(events) + 1},
			Event{
				Kind:    EventStepSucceeded,
				Step:    name,
				Seq:     len(events) + 2,
				Attempt: 1,
				Changed: []string{seatsField},
			},
		)
	}

	started.Steps[steps/2].Zone = ZonePivot

	return append(events, Event{Kind: EventSagaFinished, Seq: len(events) + 1, Status: StatusCompleted})
}

func Benchmark_foldStepsSharingChanged(b *testing.B) {
	events := benchEvents(20)

	for b.Loop() {
		foldStepsSharingChanged(events)
	}
}

func Benchmark_pendingOf(b *testing.B) {
	events := benchEvents(20)

	for b.Loop() {
		pendingOf(events)
	}
}
