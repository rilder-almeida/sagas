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
	panicBoom      = "panic: boom"
	goexitText     = "sagas: call ended by runtime.Goexit"
)

func trailOf(kind EventKind, step string) string { return strings.TrimSpace(string(kind) + " " + step) }
func startOf(step string) string                 { return trailOf(EventStepStarted, step) }
func doneOf(step string) string                  { return trailOf(EventStepSucceeded, step) }
func failedB() string                            { return failedOf("b") }
func failedOf(step string) string                { return trailOf(EventStepFailed, step) }
func lostOf(step string) string                  { return trailOf(EventStepUnknown, step) }
func skipOf(step string) string                  { return trailOf(EventStepSkipped, step) }

var errJournalDown = errors.New("journal down")

func fails() *actStep { return act("b", alwaysFails) }

func exits(name string) *actStep {
	return act(name, func(context.Context, *tripState) error {
		runtime.Goexit()

		return nil
	})
}

func napping(name, hotel string) *actStep {
	return act(name, func(_ context.Context, s *tripState) error {
		time.Sleep(pause)

		s.Hotel = hotel

		return nil
	})
}

func waiting(name string) *actStep {
	return act(name, func(ctx context.Context, _ *tripState) error {
		<-ctx.Done()

		return ctx.Err()
	})
}

func forwardRows() []runRow {
	return []runRow{
		completedGraphRow(),
		sharedStepRow(),
		skippedStepRow(),
		predicateTrueRow(),
		noPivotZonesRow(),
		pivotZonesRow(),
		concurrentRunsRow(),
	}
}

func completedGraphRow() runRow {
	return runRow{
		name: "[Success] When: the graph has parallel branches ; Should: run both branches at once and complete",
		steps: func(rec *recorder) Flow {
			var both sync.WaitGroup

			both.Add(2)

			branch := func(name string) Flow {
				return act(name, func(context.Context, *tripState) error {
					rec.add(name)
					both.Done()
					both.Wait()

					return nil
				})
			}

			return Sequence(rec.step("a"), Parallel(branch("b"), branch("c")), rec.booking("d"))
		},
		wantStatus: StatusCompleted,
		wantTrail: afterA(
			together(startOf("b"), startOf("c"), doneOf("b"), doneOf("c")),
			seq(startOf("d"), doneOf("d"), closed),
		),
		wantSteps: map[string]StepStatus{
			"a": StepSucceeded, "b": StepSucceeded, "c": StepSucceeded, "d": StepSucceeded,
		},
		wantOrder: [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}},
		wantState: tripState{Hotel: hotelRitz},
	}
}

func sharedStepRow() runRow {
	return runRow{
		name: "[Success] When: one step ends two chains ; Should: run it once after both",
		steps: func(rec *recorder) Flow {
			c := rec.booking("c")

			return Parallel(Sequence(rec.step("a"), c), Sequence(rec.step("b"), c))
		},
		wantStatus: StatusCompleted,
		wantTrail: slices.Concat(
			seq(opened),
			together(startOf("a"), startOf("b"), doneOf("a"), doneOf("b")),
			seq(startOf("c"), doneOf("c"), closed),
		),
		wantSteps: map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded, "c": StepSucceeded},
		wantOrder: [][2]string{{"a", "c"}, {"b", "c"}},
		wantState: tripState{Hotel: hotelRitz},
	}
}

func skippedStepRow() runRow {
	return runRow{
		name: "[Success] When: an Enabled method returns false ; Should: skip the step and still run its dependents",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.bookingIf("b", "never", rec.when(false, "b")), rec.step("c"))
		},
		wantStatus: StatusCompleted,
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSkipped, "c": StepSucceeded},
		wantOrder:  [][2]string{{"a", ifB}, {ifB, "c"}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), skipOf("b"), startOf("c"), doneOf("c"),
			closed,
		),
	}
}

func predicateTrueRow() runRow {
	return runRow{
		name: "[Success] When: an Enabled method returns true ; Should: call it before Execute and run the step",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.booking("a"), rec.bookingIf("b", "plaza", rec.when(true, "b")))
		},
		wantStatus: StatusCompleted,
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded},
		wantOrder:  [][2]string{{"a", ifB}, {ifB, "b"}},
		wantTrail:  seq(opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), closed),
		wantState:  tripState{Hotel: "plaza"},
	}
}

func noPivotZonesRow() runRow {
	return runRow{
		name: "[Success] When: the saga has no pivot ; Should: record the compensable zone for every step",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.step("b"))
		},
		wantStatus: StatusCompleted,
		wantTrail:  seq(opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), closed),
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded},
		wantOrder:  [][2]string{{"a", "b"}},
		wantZones:  []StepZone{{Name: "a", Zone: storedCompText}, {Name: "b", Zone: storedCompText}},
	}
}

func pivotZonesRow() runRow {
	return runRow{
		name: "[Success] When: the saga has a pivot ; Should: record a zone per step and run the forward-only step",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.pivot("p"), rec.step("z"))
		},
		wantStatus: StatusCompleted,
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z"), doneOf("z"), closed,
		),
		wantSteps: map[string]StepStatus{"a": StepSucceeded, "p": StepSucceeded, "z": StepSucceeded},
		wantOrder: [][2]string{{"a", "p"}, {"p", "z"}},
		wantZones: []StepZone{
			{Name: "a", Zone: ZoneCompensable}, {Name: "p", Zone: ZonePivot}, {Name: "z", Zone: ZoneForwardOnly},
		},
	}
}

func concurrentRunsRow() runRow {
	return runRow{
		name: "[Success] When: one saga runs many times at once ; Should: complete every run on its own state",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.booking("b"))
		},
		runs:       8,
		concurrent: true,
		wantStatus: StatusCompleted,
		wantTrail:  seq(opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), closed),
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
		predicateAppendRow(),
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
		name: "[Fail] When: a step fails ; Should: record the failure, start nothing after it and return its Error",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), fails(), rec.step("c"))
		},
		wantStatus:     StatusUnfinished,
		wantKinds:      map[string]*KindError{"b": ErrFailed},
		wantErrText:    map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"b": 3},
		wantPreviousIs: map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
		wantErrs:       []error{errAlwaysFails},
		wantSteps:      map[string]StepStatus{"a": StepSucceeded, "b": StepFailed, "c": StepNotStarted},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("b"), failedB()),
	}
}

func goexitRow() runRow {
	return runRow{
		name: "[Fail] When: an action ends by runtime.Goexit ; Should: record an unknown step and return",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), exits("b"))
		},
		wantStatus:   StatusUnfinished,
		wantKinds:    map[string]*KindError{"b": ErrUnknown},
		wantErrText:  map[string]string{"b": goexitText},
		wantAttempts: map[string]int{"b": 1},
		wantErrs:     []error{errGoexit},
		wantSteps:    map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown},
		wantPending:  []string{"b"},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b")),
	}
}

func predicatePanicRow() runRow {
	return runRow{
		name: "[Fail] When: an Enabled method panics ; Should: fail the step without a started event",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.stepIf("b", func(*tripState) bool { panic("boom") }))
		},
		wantStatus:   StatusUnfinished,
		wantKinds:    map[string]*KindError{"b": ErrPanic},
		wantErrText:  map[string]string{"b": panicBoom},
		wantAttempts: map[string]int{"b": 0},
		wantText:     []string{panicBoom},
		wantSteps:    map[string]StepStatus{"a": StepSucceeded, "b": StepFailed},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), failedB()),
	}
}

func predicateGoexitRow() runRow {
	return runRow{
		name: "[Fail] When: an Enabled method ends by runtime.Goexit ; Should: fail the step without a started event",
		steps: func(rec *recorder) Flow {
			leave := func(*tripState) bool {
				runtime.Goexit()

				return true
			}

			return Sequence(rec.step("a"), rec.stepIf("b", leave))
		},
		wantStatus:   StatusUnfinished,
		wantKinds:    map[string]*KindError{"b": ErrFailed},
		wantErrText:  map[string]string{"b": goexitText},
		wantAttempts: map[string]int{"b": 0},
		wantErrs:     []error{errGoexit},
		wantSteps:    map[string]StepStatus{"a": StepSucceeded, "b": StepFailed},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), failedB()),
	}
}

func siblingCancelRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails beside a running sibling ; Should: cancel it, wait for it, start nothing",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), Parallel(fails(), waiting("c")), rec.step("d"))
		},
		wantStatus:     StatusUnfinished,
		wantKinds:      map[string]*KindError{"b": ErrFailed, "c": ErrUnknown},
		wantErrText:    map[string]string{"b": errAlwaysFails.Error(), "c": context.Canceled.Error()},
		wantAttempts:   map[string]int{"b": 3, "c": 1},
		wantPreviousIs: map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
		wantCauseText:  map[string]string{"c": quotedB},
		wantTrail:      afterA(together(startOf("b"), startOf("c"), failedB(), lostOf("c"))),
		wantErrs:       []error{errAlwaysFails, context.Canceled},
		wantSteps: map[string]StepStatus{
			"a": StepSucceeded,
			"b": StepFailed,
			"c": StepUnknown,
			"d": StepNotStarted,
		},
		wantPending: []string{"c"},
	}
}

func conflictRow() runRow {
	return runRow{
		name: "[Fail] When: two parallel steps change the same field ; Should: fail the later one with ErrConflict",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), Parallel(rec.booking("b"), napping("c", "plaza")))
		},
		wantStatus:   StatusUnfinished,
		wantKinds:    map[string]*KindError{"c": ErrConflict},
		wantErrText:  map[string]string{"c": `field Hotel changed by step "c" after step "b"`},
		wantAttempts: map[string]int{"c": 1},
		wantTrail:    afterA(together(startOf("b"), startOf("c"), doneOf("b"), failedOf("c"))),
		wantErrs:     []error{ErrConflict},
		wantText:     []string{`"b"`, `"c"`},
		wantSteps:    map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded, "c": StepFailed},
		wantState:    tripState{Hotel: hotelRitz},
	}
}

func stoppedBeforeStartRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails before another step's Enabled says go ; Should: not start that step",
		steps: func(rec *recorder) Flow {
			late := func(*tripState) bool {
				time.Sleep(pause)

				return true
			}

			return Sequence(rec.step("a"), Parallel(fails(), rec.stepIf("c", late)))
		},
		wantStatus:     StatusUnfinished,
		wantKinds:      map[string]*KindError{"b": ErrFailed},
		wantErrText:    map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"b": 3},
		wantPreviousIs: map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
		wantErrs:       []error{errAlwaysFails},
		wantSteps:      map[string]StepStatus{"a": StepSucceeded, "b": StepFailed, "c": StepNotStarted},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("b"), failedB()),
	}
}

func rejectionRow() runRow {
	return runRow{
		name: "[Fail] When: the journal rejects a mid-run event ; Should: stop and fail with ErrConcurrentWriter",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.booking("a"), rec.step("b"))
		},
		script:      map[int]error{3: ErrConcurrentWriter},
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]StepStatus{"a": StepUnknown, "b": StepNotStarted},
		wantPending: []string{"a"},
		wantTrail:   seq(opened, startOf("a")),
		wantState:   tripState{Hotel: hotelRitz},
	}
}

func lateSiblingRow() runRow {
	return runRow{
		name: "[Fail] When: a sibling finishes after a rejection ; Should: commit and append nothing for it",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), Parallel(rec.step("b"), napping("c", "late")))
		},
		script:      map[int]error{6: ErrConcurrentWriter},
		wantStatus:  StatusUnfinished,
		wantTrail:   afterA(together(startOf("b"), startOf("c"))),
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown, "c": StepUnknown},
		wantPending: []string{"b", "c"},
	}
}

func predicateAppendRow() runRow {
	return runRow{
		name: "[Fail] When: an Enabled method fails and its append fails ; Should: keep the step not started",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.stepIf("b", func(*tripState) bool { panic("boom") }))
		},
		script:         map[int]error{4: errJournalDown},
		wantStatus:     StatusUnfinished,
		wantKinds:      map[string]*KindError{"b": ErrPanic},
		wantErrText:    map[string]string{"b": panicBoom},
		wantAttempts:   map[string]int{"b": 0},
		wantUnrecorded: []string{"b"},
		wantErrs:       []error{ErrJournal, errJournalDown},
		wantText:       []string{panicBoom},
		wantSteps:      map[string]StepStatus{"a": StepSucceeded, "b": StepNotStarted},
		wantTrail:      seq(opened, startOf("a"), doneOf("a")),
	}
}

func outcomeAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a success fails ; Should: leave the step unknown, return ErrJournal",
		steps: func(rec *recorder) Flow {
			return rec.booking("a")
		},
		script:      map[int]error{3: errJournalDown},
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{ErrJournal, errJournalDown},
		wantSteps:   map[string]StepStatus{"a": StepUnknown},
		wantPending: []string{"a"},
		wantTrail:   seq(opened, startOf("a")),
		wantState:   tripState{Hotel: hotelRitz},
	}
}

func failureAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a failure fails ; Should: join the Error with ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), fails())
		},
		script:         map[int]error{5: errJournalDown},
		wantStatus:     StatusUnfinished,
		wantKinds:      map[string]*KindError{"b": ErrFailed},
		wantErrText:    map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"b": 3},
		wantPreviousIs: map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
		wantUnrecorded: []string{"b"},
		wantErrs:       []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantSteps:      map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown},
		wantPending:    []string{"b"},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("b")),
	}
}

func intentAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the journal fails the intent append ; Should: not start the step and return ErrJournal",
		steps: func(rec *recorder) Flow {
			return rec.step("a")
		},
		script:     map[int]error{2: errJournalDown},
		wantStatus: StatusUnfinished,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]StepStatus{"a": StepNotStarted},
		wantTrail:  seq(opened),
	}
}

func predicateIntentRow() runRow {
	return runRow{
		name: "[Fail] When: the intent append of an Enabled step fails ; Should: not run it, return ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.bookingIf("b", "never", rec.when(true, "b")))
		},
		script:     map[int]error{4: errJournalDown},
		wantStatus: StatusUnfinished,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepNotStarted},
		wantTrail:  seq(opened, startOf("a"), doneOf("a")),
	}
}

func finishedAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append fails ; Should: stay unfinished, return ErrJournal",
		steps: func(rec *recorder) Flow {
			return rec.booking("a")
		},
		script:     map[int]error{4: errJournalDown},
		wantStatus: StatusUnfinished,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded},
		wantTrail:  seq(opened, startOf("a"), doneOf("a")),
		wantState:  tripState{Hotel: hotelRitz},
	}
}

func finishedRejectedRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append is rejected ; Should: stay unfinished, return ErrConcurrentWriter",
		steps: func(rec *recorder) Flow {
			return rec.step("a")
		},
		script:      map[int]error{4: fmt.Errorf("%w: other writer", ErrConcurrentWriter)},
		wantStatus:  StatusUnfinished,
		wantTrail:   seq(opened, startOf("a"), doneOf("a")),
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
		wantLabel *KindError
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
			pred:      func(*tripState) bool { panic(errBoom) },
			wantPanic: true,
			wantErrIs: errBoom,
			wantLabel: ErrPanic,
		},
		{
			name:      "[Fail] When: the base does not decode ; Should: return ErrInvalidState, skip the predicate",
			pred:      func(*tripState) bool { panic("never called") },
			corrupt:   true,
			wantErrIs: ErrInvalidState,
			wantLabel: ErrInvalidState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := baseOf(t, tripState{})
			if tt.corrupt {
				base.fields[0] = []byte("{")
			}

			run, label, err := judge(tt.pred, base)

			assert.Equal(t, tt.wantRun, run)
			assert.Same(t, tt.wantLabel, label)

			var panicked *PanicError

			assert.Equal(t, tt.wantPanic, errors.As(err, &panicked))
			require.ErrorIs(t, err, tt.wantErrIs)
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

type strayReport struct{}

func (strayReport) isReport() {}

func Test_runner_receive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		report    report
		wantPanic string
	}{
		{
			name:      "[Fail] When: the report is nil ; Should: panic naming the missing type",
			report:    nil,
			wantPanic: "sagas: forward received an unknown report of type <nil>",
		},
		{
			name:      "[Fail] When: the report is a pointer to a verdict ; Should: panic naming the pointer type",
			report:    &verdict{},
			wantPanic: "sagas: forward received an unknown report of type *sagas.verdict",
		},
		{
			name:      "[Fail] When: the report is a type the coordinator does not know ; Should: panic naming it",
			report:    strayReport{},
			wantPanic: "sagas: forward received an unknown report of type sagas.strayReport",
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
