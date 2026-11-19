package sagas

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	ifB            = "if:b"
	storedCompText = "compensable"
)

func forwardRows() []runRow {
	return []runRow{
		completedGraphRow(),
		skippedStepRow(),
		predicateTrueRow(),
		noPivotZonesRow(),
		pivotZonesRow(),
		givenJournalRow(),
	}
}

func completedGraphRow() runRow {
	return runRow{
		name: "[Success] When: the graph has parallel branches ; Should: run every step after its parents and complete",
		steps: func(rec *recorder) []Option {
			return []Option{
				rec.step("a"),
				rec.step("b", After("a")),
				rec.step("c", After("a")),
				rec.booking("d", "ritz", After("b", "c")),
			}
		},
		wantStatus: StatusCompleted,
		wantSteps: map[string]StepStatus{
			"a": StepSucceeded, "b": StepSucceeded, "c": StepSucceeded, "d": StepSucceeded,
		},
		wantOrder: [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}},
		wantState: tripState{Hotel: "ritz"},
	}
}

func skippedStepRow() runRow {
	return runRow{
		name: "[Success] When: an If predicate is false ; Should: skip the step and still run its dependents",
		steps: func(rec *recorder) []Option {
			return []Option{
				rec.step("a"),
				rec.booking("b", "never", After("a"), rec.when(false, "b")),
				rec.step("c", After("b")),
			}
		},
		wantStatus: StatusCompleted,
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSkipped, "c": StepSucceeded},
		wantOrder:  [][2]string{{"a", ifB}, {ifB, "c"}},
	}
}

func predicateTrueRow() runRow {
	return runRow{
		name: "[Success] When: an If predicate is true ; Should: evaluate it before the action and run the step",
		steps: func(rec *recorder) []Option {
			return []Option{
				rec.booking("a", "ritz"),
				rec.booking("b", "plaza", After("a"), rec.when(true, "b")),
			}
		},
		wantStatus: StatusCompleted,
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded},
		wantOrder:  [][2]string{{"a", ifB}, {ifB, "b"}},
		wantState:  tripState{Hotel: "plaza"},
	}
}

func noPivotZonesRow() runRow {
	return runRow{
		name: "[Success] When: the saga has no pivot ; Should: record the compensable zone for every step",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a"), rec.step("b", After("a"))}
		},
		wantStatus: StatusCompleted,
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded},
		wantOrder:  [][2]string{{"a", "b"}},
		wantZones:  []StepZone{{Name: "a", Zone: storedCompText}, {Name: "b", Zone: storedCompText}},
	}
}

func pivotZonesRow() runRow {
	return runRow{
		name: "[Success] When: the saga has a pivot ; Should: record a zone per step and run the forward-only step",
		steps: func(rec *recorder) []Option {
			return []Option{
				rec.step("a"),
				rec.step("p", After("a"), Pivot()),
				rec.step("z", After("p")),
			}
		},
		wantStatus: StatusCompleted,
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "p": StepSucceeded, "z": StepSucceeded},
		wantOrder:  [][2]string{{"a", "p"}, {"p", "z"}},
		wantZones: []StepZone{
			{Name: "a", Zone: ZoneCompensable}, {Name: "p", Zone: ZonePivot}, {Name: "z", Zone: ZoneForwardOnly},
		},
	}
}

func givenJournalRow() runRow {
	return runRow{
		name: "[Success] When: the caller passes a journal and an execution ID ; Should: record it there",
		steps: func(rec *recorder) []Option {
			return []Option{rec.booking("a", "ritz")}
		},
		journal:    func() Journal { return newMemoryJournal() },
		runOpts:    []RunOption{WithExecutionID("order-7")},
		wantStatus: StatusCompleted,
		wantSteps:  map[string]StepStatus{"a": StepSucceeded},
		wantState:  tripState{Hotel: "ritz"},
	}
}

func Test_ready(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		parents  []int
		statuses []runStatus
		want     bool
	}{
		{
			name:     "[Success] When: the step has no parents ; Should: be ready",
			statuses: []runStatus{runNotStarted},
			want:     true,
		},
		{
			name:     "[Success] When: every parent succeeded ; Should: be ready",
			parents:  []int{0, 1},
			statuses: []runStatus{runSucceeded, runSucceeded, runNotStarted},
			want:     true,
		},
		{
			name:     "[Success] When: a parent was skipped ; Should: be ready",
			parents:  []int{0, 1},
			statuses: []runStatus{runSkipped, runSucceeded, runNotStarted},
			want:     true,
		},
		{
			name:     "[Fail] When: a parent is running ; Should: not be ready",
			parents:  []int{0, 1},
			statuses: []runStatus{runSucceeded, runRunning, runNotStarted},
		},
		{
			name:     "[Fail] When: a parent has not started ; Should: not be ready",
			parents:  []int{0},
			statuses: []runStatus{runNotStarted, runNotStarted},
		},
		{
			name:     "[Fail] When: a parent failed ; Should: not be ready",
			parents:  []int{0},
			statuses: []runStatus{runFailed, runNotStarted},
		},
		{
			name:     "[Fail] When: a parent is unknown ; Should: not be ready",
			parents:  []int{0},
			statuses: []runStatus{runUnknown, runNotStarted},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runs := make([]stepRun, len(tt.statuses))
			for i, s := range tt.statuses {
				runs[i].status = s
			}

			assert.Equal(t, tt.want, ready(tt.parents, runs))
		})
	}
}
