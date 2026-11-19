package sagas

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	opened         = string(EventSagaStarted)
	closed         = string(EventSagaFinished)
	ifB            = "if:b"
	storedCompText = "compensable"
	hotelRitz      = "ritz"
	pause          = time.Second
)

func trailOf(kind EventKind, step string) string { return strings.TrimSpace(string(kind) + " " + step) }
func startOf(step string) string                 { return trailOf(EventStepStarted, step) }
func doneOf(step string) string                  { return trailOf(EventStepSucceeded, step) }
func failedB() string                            { return trailOf(EventStepFailed, "b") }
func lostOf(step string) string                  { return trailOf(EventStepUnknown, step) }
func skipOf(step string) string                  { return trailOf(EventStepSkipped, step) }

var errJournalDown = errors.New("journal down")

func scripted(errs map[int]error) func() Journal {
	return func() Journal { return &scriptJournal{errs: errs} }
}

func fails(opts ...StepOption) Option {
	return Step("b", alwaysFails, opts...)
}

func exits(name string, opts ...StepOption) Option {
	return Step(name, func(context.Context, *tripState) error {
		runtime.Goexit()

		return nil
	}, opts...)
}

func napping(name, hotel string, opts ...StepOption) Option {
	return Step(name, func(_ context.Context, s *tripState) error {
		time.Sleep(pause)

		s.Hotel = hotel

		return nil
	}, opts...)
}

func forwardRows() []runRow {
	return []runRow{
		completedGraphRow(),
		skippedStepRow(),
		predicateTrueRow(),
		noPivotZonesRow(),
		pivotZonesRow(),
		givenJournalRow(),
		defaultJournalRow(),
		concurrentRunsRow(),
	}
}

func completedGraphRow() runRow {
	return runRow{
		name: "[Success] When: the graph has parallel branches ; Should: run both branches at once and complete",
		steps: func(rec *recorder) []Option {
			var both sync.WaitGroup

			both.Add(2)

			branch := func(name string) Option {
				return Step(name, func(context.Context, *tripState) error {
					rec.add(name)
					both.Done()
					both.Wait()

					return nil
				}, After("a"))
			}

			return []Option{rec.step("a"), branch("b"), branch("c"), rec.booking("d", hotelRitz, After("b", "c"))}
		},
		wantStatus: StatusCompleted,
		wantSteps: map[string]StepStatus{
			"a": StepSucceeded, "b": StepSucceeded, "c": StepSucceeded, "d": StepSucceeded,
		},
		wantOrder: [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}},
		wantState: tripState{Hotel: hotelRitz},
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
		wantTrail: []string{
			opened, startOf("a"), doneOf("a"), skipOf("b"), startOf("c"), doneOf("c"),
			closed,
		},
	}
}

func predicateTrueRow() runRow {
	return runRow{
		name: "[Success] When: an If predicate is true ; Should: evaluate it before the action and run the step",
		steps: func(rec *recorder) []Option {
			return []Option{
				rec.booking("a", hotelRitz),
				rec.booking("b", "plaza", After("a"), rec.when(true, "b")),
			}
		},
		wantStatus: StatusCompleted,
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded},
		wantOrder:  [][2]string{{"a", ifB}, {ifB, "b"}},
		wantTrail:  []string{opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), closed},
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
			return []Option{rec.booking("a", hotelRitz)}
		},
		journal:       func() Journal { return newMemoryJournal() },
		runOpts:       []RunOption{WithExecutionID("order-7")},
		wantStatus:    StatusCompleted,
		wantExecution: "order-7",
		wantSteps:     map[string]StepStatus{"a": StepSucceeded},
		wantState:     tripState{Hotel: hotelRitz},
	}
}

func defaultJournalRow() runRow {
	return runRow{
		name: "[Success] When: two runs share an execution ID and no journal ; Should: complete both",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a")}
		},
		runOpts:       []RunOption{WithExecutionID("order-7")},
		runs:          2,
		wantStatus:    StatusCompleted,
		wantExecution: "order-7",
		wantSteps:     map[string]StepStatus{"a": StepSucceeded},
	}
}

func concurrentRunsRow() runRow {
	return runRow{
		name: "[Success] When: one saga runs many times at once ; Should: complete every run on its own state",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a"), rec.booking("b", hotelRitz, After("a"))}
		},
		runs:       8,
		concurrent: true,
		wantStatus: StatusCompleted,
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded},
		wantState:  tripState{Hotel: hotelRitz},
	}
}

func stopRows() []runRow {
	return slices.Concat(failureRows(), journalRows())
}

func failureRows() []runRow {
	return []runRow{
		stepFailsRow(),
		goexitRow(),
		predicatePanicRow(),
		predicateGoexitRow(),
		siblingCancelRow(),
		conflictRow(),
		stoppedBeforeStartRow(),
	}
}

func journalRows() []runRow {
	return []runRow{
		rejectionRow(),
		lateSiblingRow(),
		latePredicateRow(),
		outcomeAppendRow(),
		failureAppendRow(),
		intentAppendRow(),
		predicateIntentRow(),
		finishedAppendRow(),
		finishedRejectedRow(),
	}
}

func stepFailsRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails ; Should: record the failure, start nothing after it and return its StepError",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a"), fails(After("a")), rec.step("c", After("b"))}
		},
		wantStatus: StatusUnfinished,
		wantErrs:   []error{errAlwaysFails},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepFailed, "c": StepNotStarted},
		wantTrail:  []string{opened, startOf("a"), doneOf("a"), startOf("b"), failedB()},
	}
}

func goexitRow() runRow {
	return runRow{
		name: "[Fail] When: an action ends by runtime.Goexit ; Should: record an unknown step and return",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a"), exits("b", After("a"))}
		},
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{errGoexit},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown},
		wantPending: []string{"b"},
		wantTrail:   []string{opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b")},
	}
}

func predicatePanicRow() runRow {
	return runRow{
		name: "[Fail] When: an If predicate panics ; Should: fail the step without a started event",
		steps: func(rec *recorder) []Option {
			boom := If(func(*tripState) bool { panic("boom") })

			return []Option{rec.step("a"), rec.step("b", After("a"), boom)}
		},
		wantStatus: StatusUnfinished,
		wantText:   []string{"panic: boom"},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepFailed},
		wantTrail:  []string{opened, startOf("a"), doneOf("a"), failedB()},
	}
}

func predicateGoexitRow() runRow {
	return runRow{
		name: "[Fail] When: an If predicate ends by runtime.Goexit ; Should: fail the step without a started event",
		steps: func(rec *recorder) []Option {
			leave := If(func(*tripState) bool {
				runtime.Goexit()

				return true
			})

			return []Option{rec.step("a"), rec.step("b", After("a"), leave)}
		},
		wantStatus: StatusUnfinished,
		wantErrs:   []error{errGoexit},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepFailed},
		wantTrail:  []string{opened, startOf("a"), doneOf("a"), failedB()},
	}
}

func siblingCancelRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails beside a running sibling ; Should: cancel it, wait for it, start nothing",
		steps: func(rec *recorder) []Option {
			waits := Step("c", func(ctx context.Context, _ *tripState) error {
				<-ctx.Done()

				return ctx.Err()
			}, After("a"))

			return []Option{rec.step("a"), fails(After("a")), waits, rec.step("d", After("b", "c"))}
		},
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{errAlwaysFails, context.Canceled},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepFailed, "c": StepUnknown, "d": StepNotStarted},
		wantPending: []string{"c"},
	}
}

func conflictRow() runRow {
	return runRow{
		name: "[Fail] When: two parallel steps change the same field ; Should: fail the later one with ErrConflict",
		steps: func(rec *recorder) []Option {
			return []Option{
				rec.step("a"),
				rec.booking("b", hotelRitz, After("a")),
				napping("c", "plaza", After("a")),
			}
		},
		wantStatus: StatusUnfinished,
		wantErrs:   []error{ErrConflict},
		wantText:   []string{`"b"`, `"c"`},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded, "c": StepFailed},
		wantState:  tripState{Hotel: hotelRitz},
	}
}

func stoppedBeforeStartRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails before another step's predicate says go ; Should: not start that step",
		steps: func(rec *recorder) []Option {
			late := If(func(*tripState) bool {
				time.Sleep(pause)

				return true
			})

			return []Option{rec.step("a"), fails(After("a")), rec.step("c", After("a"), late)}
		},
		wantStatus: StatusUnfinished,
		wantErrs:   []error{errAlwaysFails},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepFailed, "c": StepNotStarted},
		wantTrail:  []string{opened, startOf("a"), doneOf("a"), startOf("b"), failedB()},
	}
}

func rejectionRow() runRow {
	return runRow{
		name: "[Fail] When: the journal rejects a mid-run event ; Should: stop and fail with ErrConcurrentWriter",
		steps: func(rec *recorder) []Option {
			return []Option{rec.booking("a", hotelRitz), rec.step("b", After("a"))}
		},
		journal:     scripted(map[int]error{3: ErrConcurrentWriter}),
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]StepStatus{"a": StepUnknown, "b": StepNotStarted},
		wantPending: []string{"a"},
		wantTrail:   []string{opened, startOf("a")},
		wantState:   tripState{Hotel: hotelRitz},
	}
}

func lateSiblingRow() runRow {
	return runRow{
		name: "[Fail] When: a sibling finishes after a rejection ; Should: commit and append nothing for it",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a"), rec.step("b", After("a")), napping("c", "late", After("a"))}
		},
		journal:     scripted(map[int]error{6: ErrConcurrentWriter}),
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown, "c": StepUnknown},
		wantPending: []string{"b", "c"},
		wantTrail: []string{
			opened, startOf("a"), doneOf("a"), startOf("b"), startOf("c"),
		},
	}
}

func latePredicateRow() runRow {
	return runRow{
		name: "[Fail] When: a predicate fails after a rejection ; Should: add no StepError and not start the step",
		steps: func(rec *recorder) []Option {
			late := If(func(*tripState) bool {
				time.Sleep(pause)

				panic("late")
			})

			return []Option{rec.step("a"), rec.step("b", After("a")), rec.step("c", After("a"), late)}
		},
		journal:       scripted(map[int]error{5: ErrConcurrentWriter}),
		wantStatus:    StatusUnfinished,
		wantErrs:      []error{ErrConcurrentWriter},
		wantNotErrs:   []error{ErrJournal},
		wantNoStepErr: true,
		wantSteps:     map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown, "c": StepNotStarted},
		wantPending:   []string{"b"},
		wantTrail:     []string{opened, startOf("a"), doneOf("a"), startOf("b")},
	}
}

func outcomeAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a success fails ; Should: leave the step unknown, return ErrJournal",
		steps: func(rec *recorder) []Option {
			return []Option{rec.booking("a", hotelRitz)}
		},
		journal:     scripted(map[int]error{3: errJournalDown}),
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{ErrJournal, errJournalDown},
		wantSteps:   map[string]StepStatus{"a": StepUnknown},
		wantPending: []string{"a"},
		wantTrail:   []string{opened, startOf("a")},
		wantState:   tripState{Hotel: hotelRitz},
	}
}

func failureAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a failure fails ; Should: join the StepError with ErrJournal",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a"), fails(After("a"))}
		},
		journal:     scripted(map[int]error{5: errJournalDown}),
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown},
		wantPending: []string{"b"},
		wantTrail:   []string{opened, startOf("a"), doneOf("a"), startOf("b")},
	}
}

func intentAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the journal fails the intent append ; Should: not start the step and return ErrJournal",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a")}
		},
		journal:    scripted(map[int]error{2: errJournalDown}),
		wantStatus: StatusUnfinished,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]StepStatus{"a": StepNotStarted},
		wantTrail:  []string{opened},
	}
}

func predicateIntentRow() runRow {
	return runRow{
		name: "[Fail] When: the intent append of an If step fails ; Should: not run it, return ErrJournal",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a"), rec.booking("b", "never", After("a"), rec.when(true, "b"))}
		},
		journal:    scripted(map[int]error{4: errJournalDown}),
		wantStatus: StatusUnfinished,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepNotStarted},
		wantTrail:  []string{opened, startOf("a"), doneOf("a")},
	}
}

func finishedAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append fails ; Should: keep the completed status, return ErrJournal",
		steps: func(rec *recorder) []Option {
			return []Option{rec.booking("a", hotelRitz)}
		},
		journal:    scripted(map[int]error{4: errJournalDown}),
		wantStatus: StatusCompleted,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded},
		wantTrail:  []string{opened, startOf("a"), doneOf("a")},
		wantState:  tripState{Hotel: hotelRitz},
	}
}

func finishedRejectedRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append is rejected ; Should: stay unfinished, return ErrConcurrentWriter",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a")}
		},
		journal:     scripted(map[int]error{4: fmt.Errorf("%w: other writer", ErrConcurrentWriter)}),
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded},
	}
}

func Test_judge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pred      func(*tripState) bool
		corrupt   bool
		wantRun   bool
		wantPanic bool
		wantErrIs error
	}{
		{
			name:    "[Success] When: the predicate returns true ; Should: report go",
			pred:    func(*tripState) bool { return true },
			wantRun: true,
		},
		{
			name: "[Success] When: the predicate returns false ; Should: report skip",
			pred: func(*tripState) bool { return false },
		},
		{
			name:      "[Fail] When: the predicate panics ; Should: return a PanicError and no go",
			pred:      func(*tripState) bool { panic("boom") },
			wantPanic: true,
		},
		{
			name:      "[Fail] When: the base does not decode ; Should: return ErrInvalidState, skip the predicate",
			pred:      func(*tripState) bool { panic("never called") },
			corrupt:   true,
			wantErrIs: ErrInvalidState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := baseOf(t, tripState{})
			if tt.corrupt {
				base.fields[0] = []byte("{")
			}

			run, err := judge(tt.pred, base)

			assert.Equal(t, tt.wantRun, run)

			var panicked *PanicError

			assert.Equal(t, tt.wantPanic, errors.As(err, &panicked))

			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)
			}

			if !tt.wantPanic && tt.wantErrIs == nil {
				require.NoError(t, err)
			}
		})
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

func Test_runner_receive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		report    report
		wantPanic string
	}{
		{
			name:      "[Fail] When: the report is an int ; Should: panic naming the type",
			report:    42,
			wantPanic: "sagas: forward received an unknown report of type int",
		},
		{
			name:      "[Fail] When: the report is a string ; Should: panic naming the type",
			report:    "late",
			wantPanic: "sagas: forward received an unknown report of type string",
		},
		{
			name:      "[Fail] When: the report is nil ; Should: panic naming the missing type",
			report:    nil,
			wantPanic: "sagas: forward received an unknown report of type <nil>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := oneStep()
			require.NoError(t, err)

			r := &runner[tripState]{saga: s}
			c := &crew[tripState]{reports: make(chan report, 1)}

			c.reports <- tt.report

			assert.PanicsWithValue(t, tt.wantPanic, func() {
				r.receive(context.Background(), c)
			})
		})
	}
}
