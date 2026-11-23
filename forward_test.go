package sagas

import (
	"context"
	"errors"
	"fmt"
	"math"
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

func pivotRows() []runRow {
	return []runRow{
		pivotErrorRow(),
		pivotRetriedRow(),
		pivotGraceEndRow(),
		pivotLateFailureRow(),
		pivotPanicRow(),
		forwardOnlyFailureRow(),
		forwardOnlyGraceEndRow(),
		forwardOnlyLateFailureRow(),
		cancelBeforePivotRow(),
		cancelBeforePivotLaunchRow(),
		cancelDuringPivotRow(),
		cancelDuringForwardPredicateRow(),
		noCallerCancelRow(),
		lateCompensableFailureRow(),
		deadlineGraceEndRow(),
		journalErrorAtGraceEndRow(),
		mixedJournalErrorAtGraceEndRow(),
		causeAtGraceEndRow(),
		pivotLateSuccessRow(),
		forwardOnlyLateSuccessRow(),
		pivotCommitFailsRow(),
		pivotErrorAppendRow(),
		pivotErrorAppendTwiceRow(),
		forwardOnlyOwnFailureRow(),
		lateSiblingAfterOwnFailureRow(),
		forwardOnlyCommitFailsRow(),
		lateAfterRejectionRow(),
		lateCompensableSiblingRow(),
		lateForwardOnlyAfterRejectionRow(),
		lateSuccessAfterRejectionRow(),
	}
}

func lateForwardOnlyAfterRejectionRow() runRow {
	return runRow{
		name: "[Fail] When: a forward-only step times out and fails after the grace end once the journal rejected " +
			"an event ; Should: join ErrGraceEnded to its error and match the caller cause",
		steps: func(rec *recorder) Flow {
			z := once("z", lateFailure)
			z.AttemptTimeout = pause / 2

			return Sequence(rec.undoable("a"), rec.pivot(), Parallel(z, rec.step("y")))
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		script:         map[int]error{7: ErrConcurrentWriter},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrConcurrentWriter,
		wantKinds:      map[string]*KindError{"z": ErrFailed},
		wantFailed:     []string{"z"},
		wantErrText:    map[string]string{"z": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"z": 1},
		wantCauseIs:    map[string]error{"z": context.DeadlineExceeded},
		wantUnrecorded: []string{"z"},
		wantErrs:       []error{ErrConcurrentWriter, errAlwaysFails, ErrGraceEnded, errStop},
		wantNotErrs:    []error{ErrJournal},
		wantTextCount:  map[string]int{ErrGraceEnded.Error(): 1},
		wantSteps: map[string]stepStatus{
			"a": stepSucceeded, "p": stepSucceeded, "z": stepUnknown, "y": stepNotStarted,
		},
		wantPending: []string{"z", "y"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z")),
		wantZones: []StepZone{
			{Name: "a", Zone: ZoneCompensable, Compensable: true},
			{Name: "p", Zone: ZonePivot},
			{Name: "z", Zone: ZoneForwardOnly},
			{Name: "y", Zone: ZoneForwardOnly},
		},
	}
}

func lateSuccessAfterRejectionRow() runRow {
	return runRow{
		name: "[Fail] When: a step succeeds after the grace end once the journal rejected an event ; " +
			"Should: end ErrConcurrentWriter without matching the caller cause",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), Parallel(rec.step("b"), act("c", lateSuccess)))
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		script:      map[int]error{6: ErrConcurrentWriter},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  statusUnfinished,
		wantKind:    ErrConcurrentWriter,
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal, errStop, ErrGraceEnded},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown, "c": stepUnknown},
		wantTrail:   afterA(together(startOf("b"), startOf("c"))),
	}
}

func lateAfterRejectionRow() runRow {
	return runRow{
		name: "[Fail] When: a step returns late without success after the journal rejected an event ; " +
			"Should: end ErrConcurrentWriter and match the caller cause",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), Parallel(rec.step("b"), once("c", lateFailure)))
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		script:         map[int]error{6: ErrConcurrentWriter},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrConcurrentWriter,
		wantKinds:      map[string]*KindError{"c": ErrFailed},
		wantFailed:     []string{"c"},
		wantErrText:    map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"c": 1},
		wantCauseText:  map[string]string{"c": "journal rejected an event"},
		wantUnrecorded: []string{"c"},
		wantErrs:       []error{ErrConcurrentWriter, errAlwaysFails, errStop},
		wantNotErrs:    []error{ErrJournal},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown, "c": stepUnknown},
		wantTrail:      afterA(together(startOf("b"), startOf("c"))),
	}
}

func lateCompensableSiblingRow() runRow {
	return runRow{
		name: "[Fail] When: a step before the pivot returns late without success after its sibling stopped the run ; " +
			"Should: match the caller cause",
		steps: func(*recorder) Flow {
			return Parallel(once("x", alwaysFails), once("y", lateFailure))
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"x": ErrFailed, "y": ErrFailed},
		wantFailed:     []string{"x", "y"},
		wantErrText:    map[string]string{"x": errAlwaysFails.Error(), "y": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"x": 1, "y": 1},
		wantCauseText:  map[string]string{"y": `"x"`},
		wantUnrecorded: []string{"y"},
		wantNotErrs:    []error{ErrJournal},
		wantErrs:       []error{errAlwaysFails, ErrGraceEnded, errStop},
		wantNotKinds:   map[string]*KindError{"y": ErrGraceEnded},
		wantSteps:      map[string]stepStatus{"x": stepFailed, "y": stepUnknown},
		wantTrail: slices.Concat(
			seq(opened),
			together(startOf("x"), startOf("y"), failedOf("x")),
		),
	}
}

func lateSiblingAfterOwnFailureRow() runRow {
	return runRow{
		name: "[Fail] When: a forward-only step fails on its own before the grace end and its sibling succeeds " +
			"after it ; Should: end ErrIncomplete and match the caller cause through the sibling's lost append",
		steps: func(rec *recorder) Flow {
			y := act("y", func(ctx context.Context, s *tripState) error {
				s.Hotel = hotelRitz

				return lateSuccess(ctx, s)
			})
			z := once("z", func(context.Context, *tripState) error {
				time.Sleep(pause * 3 / 2)

				return errAlwaysFails
			})

			return Sequence(rec.undoable("a"), rec.pivot(), Parallel(y, z))
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   statusUnfinished,
		wantKind:     ErrIncomplete,
		wantKinds:    map[string]*KindError{"z": ErrFailed},
		wantFailed:   []string{"z"},
		wantErrText:  map[string]string{"z": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"z": 1},
		wantErrs:     []error{errAlwaysFails, ErrGraceEnded, errStop, context.Canceled},
		wantNotErrs:  []error{ErrCompensated, ErrJournal},
		wantSteps: map[string]stepStatus{
			"a": stepSucceeded, "p": stepSucceeded, "y": stepUnknown, "z": stepFailed,
		},
		wantPending: []string{"y", "z"},
		wantTrail: slices.Concat(
			seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p")),
			together(startOf("y"), startOf("z")),
			seq(failedOf("z")),
		),
		wantZones: []StepZone{
			{Name: "a", Zone: ZoneCompensable, Compensable: true},
			{Name: "p", Zone: ZonePivot},
			{Name: "y", Zone: ZoneForwardOnly},
			{Name: "z", Zone: ZoneForwardOnly},
		},
		wantState: tripState{Hotel: hotelRitz},
	}
}

func forwardOnlyCommitFailsRow() runRow {
	return runRow{
		name: "[Fail] When: a forward-only step succeeds and its state does not encode ; " +
			"Should: fail it with ErrInvalidState and end ErrIncomplete",
		steps: func(rec *recorder) Flow {
			z := act("z", func(_ context.Context, s *tripState) error {
				s.Rate = math.NaN()

				return nil
			})

			return Sequence(rec.undoable("a"), rec.pivot(), z)
		},
		wantStatus:   StatusIncomplete,
		wantKind:     ErrIncomplete,
		wantKinds:    map[string]*KindError{"z": ErrInvalidState},
		wantFailed:   []string{"z"},
		wantErrText:  map[string]string{"z": "NaN"},
		wantAttempts: map[string]int{"z": 1},
		wantErrs:     []error{ErrInvalidState},
		wantNotErrs:  []error{ErrCompensated},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepFailed},
		wantPending:  []string{"z"},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z"), failedOf("z"), closed,
		),
		wantZones: pivotZones("z"),
	}
}

func lateSuccess(context.Context, *tripState) error {
	time.Sleep(3 * pause)

	return nil
}

func pivotLateSuccessRow() runRow {
	return runRow{
		name: "[Fail] When: the pivot succeeds after the grace period ends ; " +
			"Should: commit it, count it unknown when its outcome append fails and end ErrIncomplete",
		steps: func(rec *recorder) Flow {
			p := paying("p", func(ctx context.Context, s *tripState) error {
				s.Hotel = hotelRitz

				return lateSuccess(ctx, s)
			})

			return Sequence(rec.undoable("a"), p, rec.step("z"))
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  statusUnfinished,
		wantKind:    ErrIncomplete,
		wantErrs:    []error{ErrGraceEnded, errStop},
		wantNotErrs: []error{ErrCompensated, ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepUnknown, "z": stepNotStarted},
		wantPending: []string{"p", "z"},
		wantRunOnly: []string{"z"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p")),
		wantZones:   pivotZones("z"),
		wantState:   tripState{Hotel: hotelRitz},
	}
}

func forwardOnlyLateSuccessRow() runRow {
	return runRow{
		name: "[Fail] When: the last forward-only step succeeds after the grace period ends ; " +
			"Should: commit it, count it unknown when its outcome append fails and end ErrIncomplete",
		steps: func(rec *recorder) Flow {
			z := act("z", func(ctx context.Context, s *tripState) error {
				s.Hotel = hotelRitz

				return lateSuccess(ctx, s)
			})

			return Sequence(rec.undoable("a"), rec.pivot(), z)
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  statusUnfinished,
		wantKind:    ErrIncomplete,
		wantErrs:    []error{ErrGraceEnded, errStop},
		wantNotErrs: []error{ErrCompensated, ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepUnknown},
		wantPending: []string{"z"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z")),
		wantZones:   pivotZones("z"),
		wantState:   tripState{Hotel: hotelRitz},
	}
}

func pivotCommitFailsRow() runRow {
	return runRow{
		name: "[Fail] When: the pivot succeeds and its state does not encode ; " +
			"Should: count it unknown, commit and compensate nothing, list it and the forward-only steps in Pending",
		steps: func(rec *recorder) Flow {
			p := paying("p", func(_ context.Context, s *tripState) error {
				s.Hotel, s.Rate = hotelRitz, math.NaN()

				return nil
			})

			return Sequence(rec.undoable("a"), p, Parallel(rec.step("y"), rec.step("z")))
		},
		wantStatus:   StatusIncomplete,
		wantKind:     ErrIncomplete,
		wantKinds:    map[string]*KindError{"p": ErrInvalidState},
		wantFailed:   []string{"p"},
		wantErrText:  map[string]string{"p": "NaN"},
		wantAttempts: map[string]int{"p": 1},
		wantErrs:     []error{ErrInvalidState},
		wantNotErrs:  []error{ErrCompensated},
		wantSteps: map[string]stepStatus{
			"a": stepSucceeded, "p": stepUnknown, "y": stepNotStarted, "z": stepNotStarted,
		},
		wantPending: []string{"p", "y", "z"},
		wantRunOnly: []string{"y", "z"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p"), lostOf("p"), closed),
		wantZones: []StepZone{
			{Name: "a", Zone: ZoneCompensable, Compensable: true},
			{Name: "p", Zone: ZonePivot},
			{Name: "y", Zone: ZoneForwardOnly},
			{Name: "z", Zone: ZoneForwardOnly},
		},
	}
}

func failingPivot() func(*recorder) Flow {
	return func(rec *recorder) Flow {
		p := paying("p", alwaysFails)
		p.Retry = NoRetry()

		return Sequence(rec.undoable("a"), p)
	}
}

func pivotErrorAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the pivot returns an error and its failure append fails once ; " +
			"Should: append the failure again before compensating and match ErrJournal",
		steps:        failingPivot(),
		script:       map[int]error{5: errJournalDown},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"p": ErrFailed},
		wantFailed:   []string{"p"},
		wantErrText:  map[string]string{"p": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"p": 1},
		wantErrs:     []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantNotErrs:  []error{ErrIncomplete},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "p": stepFailed},
		wantOrder:    [][2]string{{"a", undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), failedOf("p"), compStartOf("a"), undoneOf("a"), closed,
		),
		wantZones: []StepZone{{Name: "a", Zone: ZoneCompensable, Compensable: true}, {Name: "p", Zone: ZonePivot}},
	}
}

func pivotErrorAppendTwiceRow() runRow {
	return runRow{
		name: "[Fail] When: the pivot returns an error and its failure append fails twice ; " +
			"Should: still compensate the steps before it and match ErrJournal",
		steps:        failingPivot(),
		script:       map[int]error{5: failing(errJournalDown, 2)},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"p": ErrFailed},
		wantFailed:   []string{"p"},
		wantErrText:  map[string]string{"p": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"p": 1},
		wantErrs:     []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantNotErrs:  []error{ErrIncomplete},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "p": stepFailed},
		wantOrder:    [][2]string{{"a", undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), failedOf("p"), compStartOf("a"), undoneOf("a"), closed,
		),
		wantZones: []StepZone{{Name: "a", Zone: ZoneCompensable, Compensable: true}, {Name: "p", Zone: ZonePivot}},
	}
}

func forwardOnlyOwnFailureRow() runRow {
	return runRow{
		name: "[Fail] When: a forward-only step fails with its own error after a caller cancel and before the " +
			"grace end ; Should: end ErrIncomplete without matching the caller cause",
		steps: func(rec *recorder) Flow {
			z := once("z", func(context.Context, *tripState) error {
				time.Sleep(pause)

				return errAlwaysFails
			})

			return Sequence(rec.undoable("a"), rec.pivot(), z)
		},
		cancelAfter:  pause / 2,
		cancelCause:  errStop,
		wantStatus:   StatusIncomplete,
		wantKind:     ErrIncomplete,
		wantKinds:    map[string]*KindError{"z": ErrFailed},
		wantFailed:   []string{"z"},
		wantErrText:  map[string]string{"z": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"z": 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{errStop, context.Canceled, ErrGraceEnded},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepFailed},
		wantPending:  []string{"z"},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z"), failedOf("z"), closed,
		),
		wantZones: pivotZones("z"),
	}
}

func lateFailure(context.Context, *tripState) error {
	time.Sleep(3 * pause)

	return errAlwaysFails
}

func pivotErrorRow() runRow {
	return runRow{
		name: "[Fail] When: the pivot returns an error ; Should: compensate the steps before it",
		steps: func(rec *recorder) Flow {
			p := paying("p", alwaysFails)
			p.Retry = NoRetry()

			return Sequence(rec.undoable("a"), p)
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"p": ErrFailed},
		wantFailed:   []string{"p"},
		wantErrText:  map[string]string{"p": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"p": 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{ErrIncomplete},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "p": stepFailed},
		wantOrder:    [][2]string{{"a", undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), failedOf("p"), compStartOf("a"), undoneOf("a"), closed,
		),
		wantZones: []StepZone{{Name: "a", Zone: ZoneCompensable, Compensable: true}, {Name: "p", Zone: ZonePivot}},
	}
}

func pivotRetriedRow() runRow {
	return runRow{
		name: "[Success] When: the caller cancels while the first pivot attempt times out ; " +
			"Should: retry the pivot on the grace context and complete",
		steps: func(rec *recorder) Flow {
			count := counter()
			p := paying("p", func(ctx context.Context, _ *tripState) error {
				if count() == 1 {
					<-ctx.Done()

					return ctx.Err()
				}

				rec.add("p")

				return nil
			})
			p.AttemptTimeout, p.Retry = pause, RetryPolicy{Attempts: 2}

			return Sequence(rec.undoable("a"), p)
		},
		cancelAfter: pause / 2,
		cancelCause: errStop,
		wantStatus:  StatusCompleted,
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded},
		wantOrder:   [][2]string{{"a", "p"}},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), closed),
		wantZones:   []StepZone{{Name: "a", Zone: ZoneCompensable, Compensable: true}, {Name: "p", Zone: ZonePivot}},
	}
}

func pivotGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: the pivot stays unknown until the grace period ends ; " +
			"Should: end ErrIncomplete with ErrGraceEnded and compensate nothing",
		steps: func(rec *recorder) Flow {
			p := paying("p", blockedOnCtx)
			p.Retry = NoRetry()

			return Sequence(rec.undoable("a"), p)
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrIncomplete,
		wantKinds:      map[string]*KindError{"p": ErrUnknown},
		wantFailed:     []string{"p"},
		wantErrText:    map[string]string{"p": canceledTxt},
		wantAttempts:   map[string]int{"p": 1},
		wantCauseIs:    map[string]error{"p": ErrGraceEnded},
		wantUnrecorded: []string{"p"},
		wantErrs:       []error{ErrGraceEnded, context.Canceled, errStop},
		wantNotErrs:    []error{ErrCompensated, ErrJournal},
		wantTextCount:  map[string]int{ErrGraceEnded.Error(): 3},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "p": stepUnknown},
		wantPending:    []string{"p"},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("p")),
		wantZones:      []StepZone{{Name: "a", Zone: ZoneCompensable, Compensable: true}, {Name: "p", Zone: ZonePivot}},
	}
}

func pivotLateFailureRow() runRow {
	return runRow{
		name: "[Fail] When: the pivot ignores its context and fails after the grace period ; " +
			"Should: keep ErrFailed and join ErrGraceEnded to its error",
		steps: func(rec *recorder) Flow {
			p := paying("p", lateFailure)
			p.Retry = NoRetry()

			return Sequence(rec.step("a"), p)
		},
		opts:            []Option{WithRecoveryTimeout(pause)},
		cancelAfter:     pause,
		cancelCause:     errStop,
		wantStatus:      statusUnfinished,
		wantKind:        ErrCompensated,
		wantKinds:       map[string]*KindError{"p": ErrFailed},
		wantFailed:      []string{"p"},
		wantErrText:     map[string]string{"p": errAlwaysFails.Error()},
		wantAttempts:    map[string]int{"p": 1},
		wantCauseIs:     map[string]error{"p": ErrGraceEnded},
		wantUnrecorded:  []string{"p"},
		wantNotErrs:     []error{ErrJournal},
		wantErrs:        []error{errAlwaysFails, ErrGraceEnded, errStop},
		wantTextCount:   map[string]int{ErrGraceEnded.Error(): 4},
		wantSteps:       map[string]stepStatus{"a": stepSucceeded, "p": stepUnknown},
		wantJournalOnly: []string{"p"},
		wantTrail:       seq(opened, startOf("a"), doneOf("a"), startOf("p")),
		wantZones:       []StepZone{{Name: "a", Zone: ZoneCompensable}, {Name: "p", Zone: ZonePivot}},
	}
}

func pivotPanicRow() runRow {
	return runRow{
		name: "[Fail] When: the pivot panics ; " +
			"Should: stop retries, end ErrIncomplete without compensating and leave the forward-only step unlisted",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), paying("p", panics), rec.step("z"))
		},
		wantStatus:   StatusIncomplete,
		wantKind:     ErrIncomplete,
		wantKinds:    map[string]*KindError{"p": ErrPanic},
		wantFailed:   []string{"p"},
		wantErrText:  map[string]string{"p": panicBoom},
		wantAttempts: map[string]int{"p": 1},
		wantText:     []string{panicBoom},
		wantNotErrs:  []error{ErrCompensated},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "p": stepUnknown, "z": stepNotStarted},
		wantPending:  []string{"p"},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), startOf("p"), lostOf("p"), closed),
		wantZones:    pivotZones("z"),
	}
}

func forwardOnlyZones() []StepZone {
	return []StepZone{
		{Name: "a", Zone: ZoneCompensable, Compensable: true},
		{Name: "p", Zone: ZonePivot},
		{Name: "y", Zone: ZoneForwardOnly},
		{Name: "z", Zone: ZoneForwardOnly},
		{Name: "w", Zone: ZoneForwardOnly},
	}
}

func forwardOnlyFailureRow() runRow {
	return runRow{
		name: "[Fail] When: a forward-only step fails beside a running sibling ; " +
			"Should: let the sibling finish, start nothing after, compensate nothing, end ErrIncomplete",
		steps: func(rec *recorder) Flow {
			sibling := act("y", func(ctx context.Context, s *tripState) error {
				time.Sleep(pause)

				s.Hotel = hotelRitz

				return ctx.Err()
			})

			return Sequence(rec.undoable("a"), rec.pivot(), Parallel(sibling, once("z", alwaysFails)), rec.step("w"))
		},
		wantStatus:   StatusIncomplete,
		wantKind:     ErrIncomplete,
		wantKinds:    map[string]*KindError{"z": ErrFailed},
		wantFailed:   []string{"z"},
		wantErrText:  map[string]string{"z": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"z": 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{ErrCompensated, context.Canceled},
		wantSteps: map[string]stepStatus{
			"a": stepSucceeded, "p": stepSucceeded, "y": stepSucceeded, "z": stepFailed, "w": stepNotStarted,
		},
		wantPending: []string{"z", "w"},
		wantTrail: slices.Concat(
			seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p")),
			together(startOf("y"), startOf("z"), failedOf("z"), doneOf("y")),
			seq(closed),
		),
		wantZones: forwardOnlyZones(),
		wantState: tripState{Hotel: hotelRitz},
	}
}

func forwardOnlyGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: a forward-only step still runs when the grace period ends ; " +
			"Should: end it unknown with ErrGraceEnded and end ErrIncomplete",
		steps: func(rec *recorder) Flow {
			z := act("z", blockedOnCtx)
			z.Retry = NoRetry()

			return Sequence(rec.undoable("a"), rec.pivot(), z)
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrIncomplete,
		wantKinds:      map[string]*KindError{"z": ErrUnknown},
		wantFailed:     []string{"z"},
		wantErrText:    map[string]string{"z": canceledTxt},
		wantAttempts:   map[string]int{"z": 1},
		wantCauseIs:    map[string]error{"z": ErrGraceEnded},
		wantUnrecorded: []string{"z"},
		wantErrs:       []error{ErrGraceEnded, errStop},
		wantNotErrs:    []error{ErrCompensated, ErrJournal},
		wantTextCount:  map[string]int{ErrGraceEnded.Error(): 3},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepUnknown},
		wantPending:    []string{"z"},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z")),
		wantZones:      pivotZones("z"),
	}
}

func forwardOnlyLateFailureRow() runRow {
	return runRow{
		name: "[Fail] When: a forward-only step times out, ignores it and fails after the grace period ; " +
			"Should: keep ErrFailed and the timeout cause, and join ErrGraceEnded to its error",
		steps: func(rec *recorder) Flow {
			z := once("z", lateFailure)
			z.AttemptTimeout = pause / 2

			return Sequence(rec.undoable("a"), rec.pivot(), z)
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrIncomplete,
		wantKinds:      map[string]*KindError{"z": ErrFailed},
		wantFailed:     []string{"z"},
		wantErrText:    map[string]string{"z": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"z": 1},
		wantCauseIs:    map[string]error{"z": context.DeadlineExceeded},
		wantUnrecorded: []string{"z"},
		wantErrs:       []error{errAlwaysFails, ErrGraceEnded, errStop},
		wantNotErrs:    []error{ErrCompensated, ErrJournal},
		wantTextCount:  map[string]int{ErrGraceEnded.Error(): 3},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepUnknown},
		wantPending:    []string{"z"},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z")),
		wantZones:      pivotZones("z"),
	}
}

func cancelBeforePivotRow() runRow {
	return runRow{
		name: "[Fail] When: the caller cancels before the pivot starts ; " +
			"Should: not start the pivot and still run the compensations on the grace context",
		steps: func(rec *recorder) Flow {
			slowUndo := func(ctx context.Context, _ *tripState) error {
				time.Sleep(pause / 2)
				rec.add(undoA)

				return ctx.Err()
			}
			b := undoOnce("b", blockedOnCtx, rec.do(undoB))

			return Sequence(undoing("a", rec.do("a"), slowUndo), b, rec.pivot())
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"b": ErrUnknown},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": canceledTxt},
		wantAttempts: map[string]int{"b": 1},
		wantCauseIs:  map[string]error{"b": errStop},
		wantErrs:     []error{errStop},
		wantNotErrs:  []error{ErrGraceEnded},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "b": stepCompensated, "p": stepNotStarted},
		wantOrder:    [][2]string{{undoB, undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b"),
			compStartOf("b"), undoneOf("b"), compStartOf("a"), undoneOf("a"), closed,
		),
		wantZones: []StepZone{
			{Name: "a", Zone: ZoneCompensable, Compensable: true},
			{Name: "b", Zone: ZoneCompensable, Compensable: true},
			{Name: "p", Zone: ZonePivot},
		},
	}
}

func cancelBeforePivotLaunchRow() runRow {
	return runRow{
		name: "[Fail] When: the caller cancels while the step before the pivot runs and it succeeds ; " +
			"Should: not start the pivot and compensate that step",
		steps: func(rec *recorder) Flow {
			slow := func(context.Context, *tripState) error {
				time.Sleep(pause)
				rec.add("a")

				return nil
			}

			return Sequence(undoing("a", slow, rec.do(undoA)), rec.pivot())
		},
		cancelAfter: pause / 2,
		cancelCause: errStop,
		wantStatus:  StatusCompensated,
		wantKind:    ErrCompensated,
		wantErrs:    []error{errStop},
		wantSteps:   map[string]stepStatus{"a": stepCompensated, "p": stepNotStarted},
		wantOrder:   [][2]string{{"a", undoA}},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), compStartOf("a"), undoneOf("a"), closed),
		wantZones:   []StepZone{{Name: "a", Zone: ZoneCompensable, Compensable: true}, {Name: "p", Zone: ZonePivot}},
	}
}

func cancelDuringPivotRow() runRow {
	return runRow{
		name: "[Success] When: the caller cancels while the pivot runs ; " +
			"Should: still launch and complete the forward-only steps",
		steps: func(rec *recorder) Flow {
			p := paying("p", func(context.Context, *tripState) error {
				time.Sleep(pause)
				rec.add("p")

				return nil
			})

			return Sequence(rec.undoable("a"), p, rec.booking("z"))
		},
		cancelAfter: pause / 2,
		cancelCause: errStop,
		wantStatus:  StatusCompleted,
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepSucceeded},
		wantOrder:   [][2]string{{"p", "z"}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z"), doneOf("z"), closed,
		),
		wantZones: pivotZones("z"),
		wantState: tripState{Hotel: hotelRitz},
	}
}

func cancelDuringForwardPredicateRow() runRow {
	return runRow{
		name: "[Success] When: the caller cancels while a forward-only Enabled method runs ; " +
			"Should: still start and complete that step",
		steps: func(rec *recorder) Flow {
			slow := func(*tripState) bool {
				time.Sleep(pause)

				return true
			}

			return Sequence(rec.undoable("a"), rec.pivot(), rec.bookingIf("z", hotelRitz, slow))
		},
		cancelAfter: pause / 2,
		cancelCause: errStop,
		wantStatus:  StatusCompleted,
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepSucceeded},
		wantOrder:   [][2]string{{"p", "z"}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z"), doneOf("z"), closed,
		),
		wantZones: pivotZones("z"),
		wantState: tripState{Hotel: hotelRitz},
	}
}

func noCallerCancelRow() runRow {
	return runRow{
		name: "[Success] When: the pivot runs longer than the grace period and the caller never cancels ; " +
			"Should: start no grace clock and complete",
		steps: func(rec *recorder) Flow {
			p := paying("p", func(ctx context.Context, _ *tripState) error {
				time.Sleep(2 * DefaultRecoveryTimeout)
				rec.add("p")

				return Permanent(ctx.Err())
			})

			return Sequence(rec.step("a"), p)
		},
		wantStatus: StatusCompleted,
		wantSteps:  map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded},
		wantOrder:  [][2]string{{"a", "p"}},
		wantTrail:  seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), closed),
		wantZones:  []StepZone{{Name: "a", Zone: ZoneCompensable}, {Name: "p", Zone: ZonePivot}},
	}
}

func lateCompensableFailureRow() runRow {
	return runRow{
		name: "[Fail] When: a step before the pivot ignores its context and fails after the grace period ; " +
			"Should: not join ErrGraceEnded to its error",
		steps: func(*recorder) Flow {
			return once("a", lateFailure)
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"a": ErrFailed},
		wantFailed:     []string{"a"},
		wantErrText:    map[string]string{"a": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"a": 1},
		wantCauseIs:    map[string]error{"a": errStop},
		wantUnrecorded: []string{"a"},
		wantNotErrs:    []error{ErrJournal},
		wantErrs:       []error{errAlwaysFails, ErrGraceEnded, errStop},
		wantNotKinds:   map[string]*KindError{"a": ErrGraceEnded},
		wantSteps:      map[string]stepStatus{"a": stepUnknown},
		wantTrail:      seq(opened, startOf("a")),
	}
}

func deadlineGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: a caller deadline passes and a late failure append is refused at the grace end ; " +
			"Should: match the deadline and ErrGraceEnded, and neither context.Canceled nor ErrJournal",
		steps: func(*recorder) Flow {
			return once("a", lateFailure)
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		deadline:       pause,
		wantStatus:     statusUnfinished,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"a": ErrFailed},
		wantFailed:     []string{"a"},
		wantErrText:    map[string]string{"a": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"a": 1},
		wantCauseIs:    map[string]error{"a": context.DeadlineExceeded},
		wantUnrecorded: []string{"a"},
		wantErrs:       []error{errAlwaysFails, context.DeadlineExceeded, ErrGraceEnded},
		wantNotErrs:    []error{context.Canceled, ErrJournal},
		wantNotKinds:   map[string]*KindError{"a": ErrGraceEnded},
		wantSteps:      map[string]stepStatus{"a": stepUnknown},
		wantTrail:      seq(opened, startOf("a")),
	}
}

func journalErrorAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: the journal fails on its own after the grace end ; " +
			"Should: match ErrJournal, the journal error and ErrGraceEnded",
		steps: func(*recorder) Flow {
			return once("a", lateFailure)
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		script:         map[int]error{3: errJournalDown},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"a": ErrFailed},
		wantFailed:     []string{"a"},
		wantErrText:    map[string]string{"a": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"a": 1},
		wantCauseIs:    map[string]error{"a": errStop},
		wantUnrecorded: []string{"a"},
		wantErrs:       []error{errAlwaysFails, ErrJournal, errJournalDown, ErrGraceEnded, errStop},
		wantNotKinds:   map[string]*KindError{"a": ErrGraceEnded},
		wantSteps:      map[string]stepStatus{"a": stepUnknown},
		wantTrail:      seq(opened, startOf("a")),
	}
}

func mixedJournalErrorAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: after the grace end the journal error joins its own failure with the context error ; " +
			"Should: keep ErrJournal, the journal error and the context error, and join ErrGraceEnded",
		steps: func(*recorder) Flow {
			return once("a", lateFailure)
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		script:         map[int]error{3: errors.Join(errJournalDown, context.Canceled)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"a": ErrFailed},
		wantFailed:     []string{"a"},
		wantErrText:    map[string]string{"a": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"a": 1},
		wantCauseIs:    map[string]error{"a": errStop},
		wantUnrecorded: []string{"a"},
		wantErrs: []error{
			errAlwaysFails, ErrJournal, errJournalDown, context.Canceled, ErrGraceEnded, errStop,
		},
		wantText:     []string{ErrJournal.Error() + ": " + errJournalDown.Error()},
		wantNotKinds: map[string]*KindError{"a": ErrGraceEnded},
		wantSteps:    map[string]stepStatus{"a": stepUnknown},
		wantTrail:    seq(opened, startOf("a")),
	}
}

func causeAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: after the grace end the journal returns the cause of its context ; " +
			"Should: match ErrGraceEnded and not ErrJournal",
		steps: func(*recorder) Flow {
			return once("a", lateFailure)
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		script:         map[int]error{3: errAwaitCause},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     statusUnfinished,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"a": ErrFailed},
		wantFailed:     []string{"a"},
		wantErrText:    map[string]string{"a": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"a": 1},
		wantCauseIs:    map[string]error{"a": errStop},
		wantUnrecorded: []string{"a"},
		wantErrs:       []error{errAlwaysFails, ErrGraceEnded, errStop},
		wantNotErrs:    []error{ErrJournal},
		wantNotKinds:   map[string]*KindError{"a": ErrGraceEnded},
		wantSteps:      map[string]stepStatus{"a": stepUnknown},
		wantTrail:      seq(opened, startOf("a")),
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
		wantSteps: map[string]stepStatus{
			"a": stepSucceeded, "b": stepSucceeded, "c": stepSucceeded, "d": stepSucceeded,
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
		wantSteps: map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded, "c": stepSucceeded},
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
		wantSteps:  map[string]stepStatus{"a": stepSucceeded, "b": stepSkipped, "c": stepSucceeded},
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
		wantSteps:  map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded},
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
		wantSteps:  map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded},
		wantOrder:  [][2]string{{"a", "b"}},
		wantZones:  []StepZone{{Name: "a", Zone: storedCompText}, {Name: "b", Zone: storedCompText}},
	}
}

func pivotZonesRow() runRow {
	return runRow{
		name: "[Success] When: the saga has a pivot ; Should: record a zone per step and run the forward-only step",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.pivot(), rec.step("z"))
		},
		wantStatus: StatusCompleted,
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z"), doneOf("z"), closed,
		),
		wantSteps: map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepSucceeded},
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
		wantSteps:  map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded},
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
		outcomeDownRow(),
		undoableOutcomeDownRow(),
		failureAppendRow(),
		intentAppendRow(),
		intentClosingFailsRow(),
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
		wantStatus:     StatusCompensated,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"b": ErrFailed},
		wantFailed:     []string{"b"},
		wantErrText:    map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"b": 3},
		wantPreviousIs: map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
		wantErrs:       []error{errAlwaysFails},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepFailed, "c": stepNotStarted},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("b"), failedB(), closed),
	}
}

func goexitRow() runRow {
	return runRow{
		name: "[Fail] When: an action ends by runtime.Goexit ; Should: record an unknown step and return",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), exits("b"))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"b": ErrUnknown},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": goexitText},
		wantAttempts: map[string]int{"b": 1},
		wantErrs:     []error{errGoexit},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b"), closed),
	}
}

func predicatePanicRow() runRow {
	return runRow{
		name: "[Fail] When: an Enabled method panics ; Should: fail the step without a started event",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.stepIf("b", func(*tripState) bool { panic("boom") }))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"b": ErrPanic},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": panicBoom},
		wantAttempts: map[string]int{"b": 0},
		wantText:     []string{panicBoom},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepFailed},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), failedB(), closed),
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
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"b": ErrFailed},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": goexitText},
		wantAttempts: map[string]int{"b": 0},
		wantErrs:     []error{errGoexit},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepFailed},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), failedB(), closed),
	}
}

func siblingCancelRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails beside a running sibling ; Should: cancel it, wait for it, start nothing",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), Parallel(fails(), waiting("c")), rec.step("d"))
		},
		wantStatus:     StatusCompensated,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"b": ErrFailed, "c": ErrUnknown},
		wantFailed:     []string{"b", "c"},
		wantErrText:    map[string]string{"b": errAlwaysFails.Error(), "c": context.Canceled.Error()},
		wantAttempts:   map[string]int{"b": 3, "c": 1},
		wantPreviousIs: map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
		wantCauseText:  map[string]string{"c": quotedB},
		wantTrail:      afterA(together(startOf("b"), startOf("c"), failedB(), lostOf("c")), seq(closed)),
		wantErrs:       []error{errAlwaysFails, context.Canceled},
		wantSteps: map[string]stepStatus{
			"a": stepSucceeded,
			"b": stepFailed,
			"c": stepUnknown,
			"d": stepNotStarted,
		},
	}
}

func conflictRow() runRow {
	return runRow{
		name: "[Fail] When: two parallel steps change the same field ; Should: fail the later one with ErrConflict",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), Parallel(rec.booking("b"), napping("c", "plaza")))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"c": ErrConflict},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": `field Hotel changed by step "c" after step "b"`},
		wantAttempts: map[string]int{"c": 1},
		wantTrail:    afterA(together(startOf("b"), startOf("c"), doneOf("b"), failedOf("c")), seq(closed)),
		wantErrs:     []error{ErrConflict},
		wantText:     []string{`"b"`, `"c"`},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded, "c": stepFailed},
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
		wantStatus:     StatusCompensated,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"b": ErrFailed},
		wantFailed:     []string{"b"},
		wantErrText:    map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"b": 3},
		wantPreviousIs: map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
		wantErrs:       []error{errAlwaysFails},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepFailed, "c": stepNotStarted},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("b"), failedB(), closed),
	}
}

func rejectionRow() runRow {
	return runRow{
		name: "[Fail] When: the journal rejects a mid-run event ; Should: stop and fail with ErrConcurrentWriter",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.booking("a"), rec.step("b"))
		},
		script:      map[int]error{3: ErrConcurrentWriter},
		wantStatus:  statusUnfinished,
		wantKind:    ErrConcurrentWriter,
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepUnknown, "b": stepNotStarted},
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
		wantStatus:  statusUnfinished,
		wantKind:    ErrConcurrentWriter,
		wantTrail:   afterA(together(startOf("b"), startOf("c"))),
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown, "c": stepUnknown},
	}
}

func predicateAppendRow() runRow {
	return runRow{
		name: "[Fail] When: an Enabled method fails and its append fails ; Should: keep the step not started",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.stepIf("b", func(*tripState) bool { panic("boom") }))
		},
		script:       map[int]error{4: errJournalDown},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"b": ErrPanic},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": panicBoom},
		wantAttempts: map[string]int{"b": 0},
		wantErrs:     []error{ErrJournal, errJournalDown},
		wantText:     []string{panicBoom},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepFailed},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), failedOf("b"), closed),
	}
}

func outcomeAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a success fails ; Should: leave the step unknown, return ErrJournal",
		steps: func(rec *recorder) Flow {
			return rec.booking("a")
		},
		script:     map[int]error{3: errJournalDown},
		wantStatus: StatusCompensated,
		wantKind:   ErrCompensated,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]stepStatus{"a": stepSucceeded},
		wantTrail:  seq(opened, startOf("a"), doneOf("a"), closed),
		wantState:  tripState{Hotel: hotelRitz},
	}
}

func outcomeDownRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a step without a compensation fails for good ; " +
			"Should: leave it out of Pending",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.booking("a"), rec.step("b"))
		},
		script:     map[int]error{3: failing(errJournalDown, forever)},
		wantStatus: statusUnfinished,
		wantKind:   ErrCompensated,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]stepStatus{"a": stepUnknown, "b": stepNotStarted},
		wantTrail:  seq(opened, startOf("a")),
		wantState:  tripState{Hotel: hotelRitz},
	}
}

func undoableOutcomeDownRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a step with a compensation fails for good ; " +
			"Should: list it in Pending",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.step("b"))
		},
		script:         map[int]error{3: failing(errJournalDown, forever)},
		wantStatus:     statusUnfinished,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{comp("a"): ErrJournal},
		wantFailed:     []string{comp("a")},
		wantErrText:    map[string]string{comp("a"): errJournalDown.Error()},
		wantAttempts:   map[string]int{comp("a"): 0},
		wantUnrecorded: []string{comp("a")},
		wantErrs:       []error{ErrJournal, errJournalDown},
		wantSteps:      map[string]stepStatus{"a": stepUnknown, "b": stepNotStarted},
		wantPending:    []string{"a"},
		wantTrail:      seq(opened, startOf("a")),
	}
}

func failureAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a failure fails ; Should: join the Error with ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), fails())
		},
		script:         map[int]error{5: errJournalDown},
		wantStatus:     StatusCompensated,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"b": ErrFailed},
		wantFailed:     []string{"b"},
		wantErrText:    map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"b": 3},
		wantPreviousIs: map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
		wantErrs:       []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepFailed},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("b"), failedOf("b"), closed),
	}
}

func intentAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the journal fails the intent append ; Should: not start the step and return ErrJournal",
		steps: func(rec *recorder) Flow {
			return rec.step("a")
		},
		script:     map[int]error{2: errJournalDown},
		wantStatus: StatusCompensated,
		wantKind:   ErrCompensated,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]stepStatus{"a": stepFailed},
		wantClosed: map[string]EventError{"a": journalDownEvent()},
		wantTrail:  seq(opened, startOf("a"), failedOf("a"), closed),
	}
}

func journalDownEvent() EventError {
	return EventError{Kind: ErrJournal.Name(), Message: fmt.Errorf("%w: %w", ErrJournal, errJournalDown).Error()}
}

func intentClosingFailsRow() runRow {
	return runRow{
		name: "[Fail] When: the intent append fails and the failure append after its resent intent fails too ; " +
			"Should: leave the step started in the journal and return ErrJournal",
		steps: func(rec *recorder) Flow {
			return rec.step("a")
		},
		script:     map[int]error{2: errJournalDown, 3: errJournalDown},
		wantStatus: statusUnfinished,
		wantKind:   ErrCompensated,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]stepStatus{"a": stepUnknown},
		wantTrail:  seq(opened, startOf("a")),
	}
}

func predicateIntentRow() runRow {
	return runRow{
		name: "[Fail] When: the intent append of an Enabled step fails ; Should: not run it, return ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), rec.bookingIf("b", "never", rec.when(true, "b")))
		},
		script:     map[int]error{4: errJournalDown},
		wantStatus: StatusCompensated,
		wantKind:   ErrCompensated,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]stepStatus{"a": stepSucceeded, "b": stepFailed},
		wantClosed: map[string]EventError{"b": journalDownEvent()},
		wantTrail:  seq(opened, startOf("a"), doneOf("a"), startOf("b"), failedOf("b"), closed),
	}
}

func finishedAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append fails ; Should: stay unfinished, return ErrJournal",
		steps: func(rec *recorder) Flow {
			return rec.booking("a")
		},
		script:     map[int]error{4: errJournalDown},
		wantStatus: statusUnfinished,
		wantKind:   ErrJournal,
		wantErrs:   []error{ErrJournal, errJournalDown},
		wantSteps:  map[string]stepStatus{"a": stepSucceeded},
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
		wantStatus:  statusUnfinished,
		wantKind:    ErrConcurrentWriter,
		wantTrail:   seq(opened, startOf("a"), doneOf("a")),
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded},
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
				r.receive(context.Background(), context.Background(), c)
			})
		})
	}
}
