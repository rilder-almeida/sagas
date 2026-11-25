package sagas

import (
	"slices"
	"strconv"
	"testing"

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
		{Name: holdStep, Zone: ZoneCompensable, Compensable: true},
		{Name: bookStep, Zone: ZoneCompensable, Compensable: true},
		{Name: payStep, Zone: ZonePivot},
		{Name: shipStep, Zone: ZoneForwardOnly},
		{Name: notifyStep, Zone: ZoneForwardOnly},
		{Name: archiveStep, Zone: ZoneForwardOnly},
	}
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

func bareEvents(bare string, tail ...Event) []Event {
	steps := runSteps()
	for i := range steps {
		if steps[i].Name == bare {
			steps[i].Compensable = false
		}
	}

	return runEventsFrom(append([]Event{{Kind: EventSagaStarted, Steps: steps}}, tail...))
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
		{Name: holdStep, Zone: "compensable", Compensable: true},
		{Name: bookStep, Zone: "compensable", Compensable: true},
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
				stepResult{Name: holdStep, Status: "succeeded"},
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
				stepResult{Name: holdStep, Status: "compensated"},
				stepResult{Name: bookStep, Status: "failed"},
			),
		},
		{
			name: "[Fail] When: the stored texts say a run is suspended ; Should: report the failed and blocked steps",
			events: literalEvents(
				Event{Kind: "compensation-failed", Step: bookStep},
				Event{Kind: "compensation-blocked", Step: holdStep},
				Event{Kind: "saga-suspended", Status: StatusCompensationFailed},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: "blocked"},
				stepResult{Name: bookStep, Status: compensationFailed},
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
				Event{Kind: "saga-suspended", Status: StatusIncomplete},
			),
			want: wantSteps(
				stepResult{Name: bookStep, Status: "blocked"},
				stepResult{Name: payStep, Status: "unknown"},
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

func Test_foldStepStatuses(t *testing.T) {
	t.Parallel()

	tests := append([]foldRow{
		{
			name: "[Success] When: every step succeeds ; Should: report each step outcome",
			events: runEvents(
				Event{Kind: EventStepStarted, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepStarted, Step: bookStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepSucceeded, Step: payStep},
				Event{Kind: EventStepSucceeded, Step: shipStep},
				Event{Kind: EventStepSucceeded, Step: notifyStep},
				Event{Kind: EventStepSucceeded, Step: archiveStep},
				Event{Kind: EventSagaFinished, Status: StatusCompleted},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepSucceeded},
				stepResult{Name: bookStep, Status: stepSucceeded},
				stepResult{Name: payStep, Status: stepSucceeded},
				stepResult{Name: shipStep, Status: stepSucceeded},
				stepResult{Name: notifyStep, Status: stepSucceeded},
				stepResult{Name: archiveStep, Status: stepSucceeded},
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
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepFailed, Step: bookStep},
				Event{Kind: EventCompensationStarted, Step: holdStep},
				Event{Kind: EventCompensationSucceeded, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensated},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepCompensated},
				stepResult{Name: bookStep, Status: stepFailed},
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
				stepResult{Name: holdStep, Status: stepCompensated},
			),
		},
		{
			name: "[Fail] When: a compensation fails ; Should: report it compensation-failed",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepFailed, Step: bookStep},
				Event{Kind: EventCompensationFailed, Step: holdStep},
				Event{Kind: EventSagaSuspended, Status: StatusCompensationFailed},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepCompensationFailed},
				stepResult{Name: bookStep, Status: stepFailed},
			),
		},
		{
			name: "[Fail] When: a compensation is blocked ; Should: report it blocked",
			events: runEvents(
				Event{Kind: EventCompensationFailed, Step: bookStep},
				Event{Kind: EventCompensationBlocked, Step: holdStep},
				Event{Kind: EventSagaSuspended, Status: StatusCompensationFailed},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepBlocked},
				stepResult{Name: bookStep, Status: stepCompensationFailed},
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
				Event{Kind: EventStepUnknown, Step: payStep},
				Event{Kind: EventSagaSuspended, Status: StatusIncomplete},
			),
			want: wantSteps(
				stepResult{Name: payStep, Status: stepUnknown},
			),
		},
		{
			name: "[Fail] When: forward-only steps fail, end unknown or never start ; Should: report each one",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: payStep},
				Event{Kind: EventStepFailed, Step: shipStep},
				Event{Kind: EventStepUnknown, Step: notifyStep},
				Event{Kind: EventSagaSuspended, Status: StatusIncomplete},
			),
			want: wantSteps(
				stepResult{Name: payStep, Status: stepSucceeded},
				stepResult{Name: shipStep, Status: stepFailed},
				stepResult{Name: notifyStep, Status: stepUnknown},
			),
		},
		{
			name: "[Fail] When: the stream has no saga-finished event ; Should: report the steps it reached",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
			),
			want: wantSteps(
				stepResult{Name: holdStep, Status: stepSucceeded},
				stepResult{Name: bookStep, Status: stepSucceeded},
			),
		},
		{
			name: "[Fail] When: the pivot starts and its outcome is lost ; Should: report it unknown",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepStarted, Step: payStep},
			),
			want: wantSteps(
				stepResult{Name: bookStep, Status: stepSucceeded},
				stepResult{Name: payStep, Status: stepUnknown},
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
				stepResult{Name: holdStep, Status: stepCompensationFailed},
				stepResult{Name: bookStep, Status: stepFailed},
			),
		},
		{
			name: "[Fail] When: an event names no listed step ; Should: ignore it",
			events: runEvents(
				Event{Kind: EventStepFailed, Step: "audit"},
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

			assert.Equal(t, tt.want, foldStepStatuses(tt.events))
		})
	}
}

func Test_nextStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current stepStatus
		kind    EventKind
		want    stepStatus
	}{
		{
			name:    "[Success] When: the step starts ; Should: be unknown until an outcome",
			current: stepNotStarted, kind: EventStepStarted, want: stepUnknown,
		},
		{
			name:    "[Success] When: the step succeeds ; Should: be succeeded",
			current: stepUnknown, kind: EventStepSucceeded, want: stepSucceeded,
		},
		{
			name:    "[Success] When: the step fails ; Should: be failed",
			current: stepUnknown, kind: EventStepFailed, want: stepFailed,
		},
		{
			name:    "[Success] When: the step ends unknown ; Should: be unknown",
			current: stepNotStarted, kind: EventStepUnknown, want: stepUnknown,
		},
		{
			name:    "[Success] When: the step is skipped ; Should: be skipped",
			current: stepNotStarted, kind: EventStepSkipped, want: stepSkipped,
		},
		{
			name:    "[Success] When: the compensation starts ; Should: be compensation-failed until an outcome",
			current: stepSucceeded, kind: EventCompensationStarted, want: stepCompensationFailed,
		},
		{
			name:    "[Success] When: the compensation succeeds ; Should: be compensated",
			current: stepCompensationFailed, kind: EventCompensationSucceeded, want: stepCompensated,
		},
		{
			name:    "[Success] When: the compensation fails ; Should: be compensation-failed",
			current: stepSucceeded, kind: EventCompensationFailed, want: stepCompensationFailed,
		},
		{
			name:    "[Success] When: the compensation is blocked ; Should: be blocked",
			current: stepSucceeded, kind: EventCompensationBlocked, want: stepBlocked,
		},
		{
			name:    "[Fail] When: the event is saga-suspended ; Should: keep the current status",
			current: stepCompensationFailed, kind: EventSagaSuspended, want: stepCompensationFailed,
		},
		{
			name:    "[Fail] When: the event is saga-started ; Should: keep the current status",
			current: stepSkipped, kind: EventSagaStarted, want: stepSkipped,
		},
		{
			name:    "[Fail] When: the event is saga-finished ; Should: keep the current status",
			current: stepSucceeded, kind: EventSagaFinished, want: stepSucceeded,
		},
		{
			name:    "[Fail] When: the kind is not set ; Should: keep the current status",
			current: stepBlocked, kind: "", want: stepBlocked,
		},
		{
			name:    "[Fail] When: the kind is unknown ; Should: keep the current status",
			current: stepCompensated, kind: EventKind("compensation-halted"), want: stepCompensated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, nextStatus(tt.current, tt.kind))
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
				Event{Kind: EventSagaSuspended, Status: StatusCompensationFailed},
			),
			want: []string{holdStep, bookStep},
		},
		{
			name: "[Fail] When: the pivot ends unknown ; Should: list only the pivot",
			events: runEvents(
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepSucceeded, Step: bookStep},
				Event{Kind: EventStepUnknown, Step: payStep},
				Event{Kind: EventSagaSuspended, Status: StatusIncomplete},
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
				Event{Kind: EventSagaSuspended, Status: StatusIncomplete},
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
				Event{Kind: EventSagaSuspended, Status: StatusIncomplete},
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
				Event{Kind: EventSagaSuspended, Status: StatusCompensationFailed},
			),
			want: []string{bookStep},
		},
		{
			name: "[Success] When: a step without compensation loses its outcome ; Should: list nothing",
			events: bareEvents(bookStep,
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepStarted, Step: bookStep},
				Event{Kind: EventSagaSuspended, Status: StatusCompensationFailed},
			),
			want: nil,
		},
		{
			name: "[Success] When: a step without compensation ends unknown ; Should: list nothing",
			events: bareEvents(bookStep,
				Event{Kind: EventStepSucceeded, Step: holdStep},
				Event{Kind: EventStepUnknown, Step: bookStep},
				Event{Kind: EventCompensationSucceeded, Step: holdStep},
				Event{Kind: EventSagaFinished, Status: StatusCompensated},
			),
			want: nil,
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

func Benchmark_foldStepStatuses(b *testing.B) {
	events := benchEvents(20)

	for b.Loop() {
		foldStepStatuses(events)
	}
}

func Benchmark_pendingOf(b *testing.B) {
	events := benchEvents(20)

	for b.Loop() {
		pendingOf(events)
	}
}
