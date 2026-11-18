package sagas

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_Status_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status Status
		want   string
	}{
		{
			name:   "[Success] When: the zero status ; Should: return unfinished",
			status: StatusUnfinished, want: "unfinished",
		},
		{
			name:   "[Success] When: StatusCompleted ; Should: return completed",
			status: StatusCompleted, want: "completed",
		},
		{
			name:   "[Success] When: StatusCompensated ; Should: return compensated",
			status: StatusCompensated, want: "compensated",
		},
		{
			name:   "[Success] When: StatusCompensationFailed ; Should: return compensation failed",
			status: StatusCompensationFailed, want: "compensation failed",
		},
		{
			name:   "[Success] When: StatusIncomplete ; Should: return incomplete",
			status: StatusIncomplete, want: "incomplete",
		},
		{
			name:   "[Fail] When: the status is out of range ; Should: return Status(99)",
			status: Status(99), want: "Status(99)",
		},
		{
			name:   "[Fail] When: the status is negative ; Should: return Status(-1)",
			status: Status(-1), want: "Status(-1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.status.String())
		})
	}
}

func Test_StepStatus_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status StepStatus
		want   string
	}{
		{
			name:   "[Success] When: the zero step status ; Should: return not started",
			status: StepNotStarted, want: "not started",
		},
		{
			name:   "[Success] When: StepSucceeded ; Should: return succeeded",
			status: StepSucceeded, want: "succeeded",
		},
		{name: "[Success] When: StepFailed ; Should: return failed", status: StepFailed, want: "failed"},
		{name: "[Success] When: StepUnknown ; Should: return unknown", status: StepUnknown, want: "unknown"},
		{name: "[Success] When: StepSkipped ; Should: return skipped", status: StepSkipped, want: "skipped"},
		{
			name:   "[Success] When: StepCompensated ; Should: return compensated",
			status: StepCompensated, want: "compensated",
		},
		{
			name:   "[Success] When: StepCompensationFailed ; Should: return compensation failed",
			status: StepCompensationFailed, want: "compensation failed",
		},
		{name: "[Success] When: StepBlocked ; Should: return blocked", status: StepBlocked, want: "blocked"},
		{name: "[Success] When: StepHalted ; Should: return halted", status: StepHalted, want: "halted"},
		{
			name:   "[Fail] When: the step status is out of range ; Should: return StepStatus(99)",
			status: StepStatus(99), want: "StepStatus(99)",
		},
		{
			name:   "[Fail] When: the step status is negative ; Should: return StepStatus(-1)",
			status: StepStatus(-1), want: "StepStatus(-1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.status.String())
		})
	}
}

const (
	holdStep    = "hold"
	bookStep    = "book"
	payStep     = "pay"
	shipStep    = "ship"
	notifyStep  = "notify"
	archiveStep = "archive"
)

func runEvents(tail ...Event) []Event {
	events := append([]Event{{Kind: EventSagaStarted, Steps: []StepZone{
		{Name: holdStep, Zone: ZoneCompensable},
		{Name: bookStep, Zone: ZoneCompensable},
		{Name: payStep, Zone: ZonePivot},
		{Name: shipStep, Zone: ZoneForwardOnly},
		{Name: notifyStep, Zone: ZoneForwardOnly},
		{Name: archiveStep, Zone: ZoneForwardOnly},
	}}}, tail...)

	for i := range events {
		base := journalEvent("x1", i+1)
		events[i].Saga, events[i].Execution, events[i].Writer = base.Saga, base.Execution, base.Writer
		events[i].Seq, events[i].Time = base.Seq, base.Time
	}

	return events
}

func wantSteps(changed ...StepResult) []StepResult {
	steps := []StepResult{
		{Name: holdStep}, {Name: bookStep}, {Name: payStep}, {Name: shipStep}, {Name: notifyStep}, {Name: archiveStep},
	}

	for i := range changed {
		k := slices.IndexFunc(steps, func(s StepResult) bool { return s.Name == changed[i].Name })
		steps[k] = changed[i]
	}

	return steps
}

func Test_buildResult(t *testing.T) {
	t.Parallel()

	at := func(seq int) time.Time { return journalEvent("", seq).Time }

	tests := []struct {
		name   string
		events []Event
		want   Result
	}{
		{
			name: "[Success] When: every step succeeds ; Should: report a completed run with each outcome",
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
			want: Result{Execution: "x1", Status: StatusCompleted, Steps: wantSteps(
				StepResult{
					Name: holdStep, Status: StepSucceeded, Attempts: 1, Changed: []string{itemsField},
					Start: at(2), End: at(3),
				},
				StepResult{
					Name: bookStep, Status: StepSucceeded, Attempts: 2, Changed: []string{seatsField},
					Start: at(4), End: at(5),
				},
				StepResult{Name: payStep, Status: StepSucceeded, Attempts: 1, End: at(6)},
				StepResult{Name: shipStep, Status: StepSucceeded, Attempts: 1, End: at(7)},
				StepResult{Name: notifyStep, Status: StepSucceeded, Attempts: 1, End: at(8)},
				StepResult{Name: archiveStep, Status: StepSucceeded, Attempts: 3, End: at(9)},
			)},
		},
		{
			name: "[Success] When: steps are skipped ; Should: mark them skipped",
			events: runEvents(
				Event{Kind: EventStepSkipped, Step: holdStep},
				Event{Kind: EventStepSkipped, Step: bookStep},
				Event{Kind: EventSagaFinished, Status: StatusCompleted},
			),
			want: Result{Execution: "x1", Status: StatusCompleted, Steps: wantSteps(
				StepResult{Name: holdStep, Status: StepSkipped},
				StepResult{Name: bookStep, Status: StepSkipped},
			)},
		},
		{
			name: "[Success] When: a step fails and an earlier one is compensated ; Should: end it at its compensation",
			events: runEvents(
				Event{Kind: EventStepStarted, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: holdStep, Attempt: 1, Changed: []string{itemsField}},
				Event{Kind: EventStepFailed, Step: bookStep, Attempt: 3, Err: "sold out"},
				Event{Kind: EventCompensationStarted, Step: holdStep},
				Event{Kind: EventCompensationSucceeded, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensated},
			),
			want: Result{Execution: "x1", Status: StatusCompensated, Steps: wantSteps(
				StepResult{
					Name: holdStep, Status: StepCompensated, Attempts: 1, Changed: []string{itemsField},
					Start: at(2), End: at(6),
				},
				StepResult{Name: bookStep, Status: StepFailed, Attempts: 3, Err: "sold out", End: at(4)},
			)},
		},
		{
			name: "[Success] When: a step ends unknown and is compensated ; Should: report it compensated",
			events: runEvents(
				Event{Kind: EventStepUnknown, Step: holdStep},
				Event{Kind: EventCompensationSucceeded, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensated},
			),
			want: Result{Execution: "x1", Status: StatusCompensated, Steps: wantSteps(
				StepResult{Name: holdStep, Status: StepCompensated, End: at(3)},
			)},
		},
		{
			name: "[Fail] When: a compensation fails ; Should: report it with its compensation error",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepFailed, Step: bookStep, Err: "declined"},
				Event{Kind: EventCompensationFailed, Step: holdStep, Err: "refund down"},
				Event{Kind: EventSagaFinished, Status: StatusCompensationFailed},
			),
			want: Result{Execution: "x1", Status: StatusCompensationFailed, Steps: wantSteps(
				StepResult{Name: holdStep, Status: StepCompensationFailed, CompensationErr: "refund down", End: at(4)},
				StepResult{Name: bookStep, Status: StepFailed, Err: "declined", End: at(3)},
			)},
		},
		{
			name: "[Fail] When: a compensation is blocked ; Should: report it blocked",
			events: runEvents(
				Event{Kind: EventCompensationFailed, Step: bookStep},
				Event{Kind: EventCompensationBlocked, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensationFailed},
			),
			want: Result{Execution: "x1", Status: StatusCompensationFailed, Steps: wantSteps(
				StepResult{Name: holdStep, Status: StepBlocked, End: at(3)},
				StepResult{Name: bookStep, Status: StepCompensationFailed, End: at(2)},
			)},
		},
		{
			name: "[Fail] When: a compensation is halted ; Should: report it halted",
			events: runEvents(
				Event{Kind: EventCompensationFailed, Step: bookStep},
				Event{Kind: EventCompensationHalted, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensationFailed},
			),
			want: Result{Execution: "x1", Status: StatusCompensationFailed, Steps: wantSteps(
				StepResult{Name: holdStep, Status: StepHalted, End: at(3)},
				StepResult{Name: bookStep, Status: StepCompensationFailed, End: at(2)},
			)},
		},
		{
			name:   "[Success] When: only the saga-started event exists ; Should: list every step not started",
			events: runEvents(),
			want:   Result{Execution: "x1", Status: StatusUnfinished, Steps: wantSteps()},
		},
		{
			name: "[Fail] When: the pivot ends unknown ; Should: report it unknown and the run incomplete",
			events: runEvents(
				Event{Kind: EventStepUnknown, Step: payStep, Attempt: 4},
				Event{Kind: EventSagaFinished, Status: StatusIncomplete},
			),
			want: Result{Execution: "x1", Status: StatusIncomplete, Steps: wantSteps(
				StepResult{Name: payStep, Status: StepUnknown, Attempts: 4, End: at(2)},
			)},
		},
		{
			name: "[Fail] When: forward-only steps fail, end unknown or never start ; Should: report each one",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: payStep},
				Event{Kind: EventStepFailed, Step: shipStep},
				Event{Kind: EventStepUnknown, Step: notifyStep},
				Event{Kind: EventSagaFinished, Status: StatusIncomplete},
			),
			want: Result{Execution: "x1", Status: StatusIncomplete, Steps: wantSteps(
				StepResult{Name: payStep, Status: StepSucceeded, End: at(2)},
				StepResult{Name: shipStep, Status: StepFailed, End: at(3)},
				StepResult{Name: notifyStep, Status: StepUnknown, End: at(4)},
			)},
		},
		{
			name: "[Fail] When: the stream has no saga-finished event ; Should: report the run unfinished",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
			),
			want: Result{Execution: "x1", Status: StatusUnfinished, Steps: wantSteps(
				StepResult{Name: holdStep, Status: StepSucceeded, End: at(2)},
				StepResult{Name: bookStep, Status: StepSucceeded, End: at(3)},
			)},
		},
		{
			name: "[Fail] When: the pivot starts and its outcome is lost ; Should: report it unknown",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepStarted, Step: payStep},
			),
			want: Result{Execution: "x1", Status: StatusUnfinished, Steps: wantSteps(
				StepResult{Name: bookStep, Status: StepSucceeded, End: at(2)},
				StepResult{Name: payStep, Status: StepUnknown, Start: at(3)},
			)},
		},
		{
			name: "[Fail] When: a compensation starts and its outcome is lost ; Should: report it failed",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepFailed, Step: bookStep},
				Event{Kind: EventCompensationStarted, Step: holdStep},
			),
			want: Result{Execution: "x1", Status: StatusUnfinished, Steps: wantSteps(
				StepResult{Name: holdStep, Status: StepCompensationFailed, End: at(2)},
				StepResult{Name: bookStep, Status: StepFailed, End: at(3)},
			)},
		},
		{
			name: "[Fail] When: an event names no listed step ; Should: ignore it",
			events: runEvents(
				Event{Kind: EventStepFailed, Step: "audit", Attempt: 1},
			),
			want: Result{Execution: "x1", Status: StatusUnfinished, Steps: wantSteps()},
		},
		{
			name:   "[Fail] When: the stream is empty ; Should: return the zero Result",
			events: nil,
			want:   Result{Execution: "", Status: StatusUnfinished, Steps: nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildResult(tt.events)
			scribble(tt.events)

			assert.Equal(t, tt.want.Execution, got.Execution)
			assert.Equal(t, tt.want.Status, got.Status)
			assert.Equal(t, tt.want.Steps, got.Steps)
		})
	}
}

func Test_applyEvent(t *testing.T) {
	t.Parallel()

	at := func(seq int) time.Time { return journalEvent("", seq).Time }
	prior := func() StepResult {
		return StepResult{
			Name: holdStep, Status: StepSucceeded, Attempts: 2, Err: "late", CompensationErr: "refund late",
			Changed: []string{seatsField}, Start: at(1), End: at(2),
		}
	}
	with := func(change func(*StepResult)) StepResult {
		s := prior()
		change(&s)

		return s
	}

	tests := []struct {
		name  string
		event Event
		want  StepResult
	}{
		{
			name:  "[Success] When: the step starts ; Should: set Start and the status unknown until an outcome",
			event: Event{Kind: EventStepStarted, Attempt: 9, Err: "x", Time: at(9)},
			want:  with(func(s *StepResult) { s.Status, s.Start = StepUnknown, at(9) }),
		},
		{
			name: "[Success] When: the step succeeds ; Should: set status, attempts, error, changed fields and End",
			event: Event{
				Kind: EventStepSucceeded, Attempt: 3, Changed: []string{itemsField, chargeField}, Time: at(9),
			},
			want: with(func(s *StepResult) {
				s.Status, s.Attempts, s.Err, s.End = StepSucceeded, 3, "", at(9)
				s.Changed = []string{itemsField, chargeField}
			}),
		},
		{
			name:  "[Success] When: the step fails ; Should: set status, attempts, error, no changed fields and End",
			event: Event{Kind: EventStepFailed, Attempt: 4, Err: "no stock", Time: at(9)},
			want: with(func(s *StepResult) {
				s.Status, s.Attempts, s.Err, s.Changed, s.End = StepFailed, 4, "no stock", nil, at(9)
			}),
		},
		{
			name:  "[Success] When: the step ends unknown ; Should: set status, attempts, error and End",
			event: Event{Kind: EventStepUnknown, Attempt: 1, Err: "timeout", Time: at(9)},
			want: with(func(s *StepResult) {
				s.Status, s.Attempts, s.Err, s.Changed, s.End = StepUnknown, 1, "timeout", nil, at(9)
			}),
		},
		{
			name:  "[Success] When: the step is skipped ; Should: set only the status",
			event: Event{Kind: EventStepSkipped, Attempt: 9, Err: "x", Time: at(9)},
			want:  with(func(s *StepResult) { s.Status = StepSkipped }),
		},
		{
			name:  "[Success] When: the compensation succeeds ; Should: set status, compensation error and End",
			event: Event{Kind: EventCompensationSucceeded, Attempt: 9, Time: at(9)},
			want:  with(func(s *StepResult) { s.Status, s.CompensationErr, s.End = StepCompensated, "", at(9) }),
		},
		{
			name:  "[Success] When: the compensation fails ; Should: set status, compensation error and End",
			event: Event{Kind: EventCompensationFailed, Err: "refund timeout", Time: at(9)},
			want: with(func(s *StepResult) {
				s.Status, s.CompensationErr, s.End = StepCompensationFailed, "refund timeout", at(9)
			}),
		},
		{
			name:  "[Success] When: the compensation is blocked ; Should: set status, compensation error and End",
			event: Event{Kind: EventCompensationBlocked, Err: "waits on book", Time: at(9)},
			want: with(
				func(s *StepResult) { s.Status, s.CompensationErr, s.End = StepBlocked, "waits on book", at(9) },
			),
		},
		{
			name:  "[Success] When: the compensation is halted ; Should: set status, compensation error and End",
			event: Event{Kind: EventCompensationHalted, Time: at(9)},
			want:  with(func(s *StepResult) { s.Status, s.CompensationErr, s.End = StepHalted, "", at(9) }),
		},
		{
			name:  "[Success] When: the compensation starts ; Should: set only the status failed until an outcome",
			event: Event{Kind: EventCompensationStarted, Attempt: 9, Err: "x", Time: at(9)},
			want:  with(func(s *StepResult) { s.Status = StepCompensationFailed }),
		},
		{
			name:  "[Fail] When: the event is a saga event ; Should: change nothing",
			event: Event{Kind: EventSagaFinished, Status: StatusCompleted, Err: "x", Time: at(9)},
			want:  prior(),
		},
		{
			name:  "[Fail] When: the kind is invalid ; Should: change nothing",
			event: Event{Kind: EventInvalid, Attempt: 9, Err: "x", Time: at(9)},
			want:  prior(),
		},
		{
			name:  "[Fail] When: the kind is out of range ; Should: change nothing",
			event: Event{Kind: EventKind(99), Attempt: 9, Err: "x", Time: at(9)},
			want:  prior(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := prior()
			applyEvent(&got, &tt.event)

			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_Result_Pending(t *testing.T) {
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

			r := buildResult(tt.events)
			slices.Reverse(r.Steps)

			assert.Equal(t, tt.want, r.Pending())
		})
	}
}

func Test_Result_Events(t *testing.T) {
	t.Parallel()

	stream := func() []Event {
		return runEvents(
			Event{Kind: EventStepStarted, Step: holdStep},
			Event{Kind: EventStepSucceeded, Step: holdStep, Attempt: 1, Changed: []string{itemsField}},
			Event{Kind: EventSagaFinished, Status: StatusCompleted},
		)
	}

	tests := []struct {
		name   string
		events []Event
		mutate func(in []Event, r *Result)
	}{
		{
			name:   "[Success] When: nothing changes ; Should: return the input stream",
			events: stream(),
			mutate: func([]Event, *Result) {},
		},
		{
			name:   "[Success] When: the stream is empty ; Should: return no events",
			events: nil,
			mutate: func([]Event, *Result) {},
		},
		{
			name:   "[Success] When: the caller changes a returned stream ; Should: return the original next time",
			events: stream(),
			mutate: func(_ []Event, r *Result) { scribble(r.Events()) },
		},
		{
			name:   "[Success] When: the caller changes the input after the build ; Should: return the original",
			events: stream(),
			mutate: func(in []Event, _ *Result) { scribble(in) },
		},
		{
			name:   "[Success] When: the caller changes a step's Changed ; Should: return the original",
			events: stream(),
			mutate: func(_ []Event, r *Result) { r.Steps[0].Changed[0] = "changed" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			want := make([]Event, len(tt.events))
			for i := range tt.events {
				want[i] = *withEvent(&tt.events[i], func(*Event) {})
			}

			r := buildResult(tt.events)
			tt.mutate(tt.events, &r)

			assert.Equal(t, want, r.Events())
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

func Benchmark_buildResult(b *testing.B) {
	events := benchEvents(20)

	for b.Loop() {
		buildResult(events)
	}
}

func Benchmark_Result_Pending(b *testing.B) {
	r := buildResult(benchEvents(20))

	for b.Loop() {
		r.Pending()
	}
}
