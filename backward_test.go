package sagas

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const (
	refunded    = "refunded"
	hotelField  = "Hotel"
	undoA       = "undo:a"
	undoB       = "undo:b"
	undoC       = "undo:c"
	cReturned   = "c returned"
	undoneHotel = "undo-b"
	rateUndone  = 2
	canceledTxt = "context canceled"
)

type guardedUndoStep struct {
	Step[tripState, None]
	doer
	undoer
	checker
}

func compStartOf(step string) string  { return trailOf(EventCompensationStarted, step) }
func undoneOf(step string) string     { return trailOf(EventCompensationSucceeded, step) }
func undoFailedOf(step string) string { return trailOf(EventCompensationFailed, step) }
func blockedOf(step string) string    { return trailOf(EventCompensationBlocked, step) }

func failingUndo(
	name string,
	policy FailurePolicy,
	undo func(context.Context, tripState) error,
) *undoStep {
	s := undoOnce(name, okAction, undo)
	s.FailurePolicy = policy

	return s
}

func chainTrail(tail ...string) [][]string {
	return seq(append([]string{
		opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), startOf("c"), failedOf("c"),
	}, tail...)...)
}

func (r *recorder) undoable(name string) *undoStep {
	return undoing(name, r.do(name), r.do("undo:"+name))
}

func panicGuarded(rec *recorder) *guardedUndoStep {
	return &guardedUndoStep{Name: "b", do: rec.do("b"), undo: rec.do(undoB), pred: panicking}
}

func panics(context.Context, tripState) error { panic("boom") }

func panicking(context.Context, tripState) bool { panic("boom") }

func blockedOnCtx(ctx context.Context, _ tripState) error {
	<-ctx.Done()

	return ctx.Err()
}

func undoOnce(name string, do, undo func(context.Context, tripState) error) *undoStep {
	s := undoing(name, do, undo)
	s.RetryPolicy, s.RecoveryPolicy = NoRetry(), RecoveryPolicy{Attempts: 1}

	return s
}

func backwardRows() []runRow {
	return []runRow{
		reversedOrderRow(),
		serialUndoRow(),
		failedAndUnknownRow(),
		undoGoexitRow(),
		unknownBarrierRow(),
		siblingBarrierRow(),
		callerDoneRow(),
		callerDoneCauseRow(),
		loneFailureRow(),
		permanentRow(),
		predicateJournalRow(),
		predicateSiblingRow(),
		blockRow(),
		continueRow(),
		blockAllRow(),
		blockAcrossRow(),
		blockThenBlockAllRow(),
		blockAllAtOnceRow(),
		undoPanicRow(),
		undoOwnFieldRow(),
		undoIntentRow(),
		journalOutageRow(),
		undoOutcomeRow(),
		undoOutcomeTwiceRow(),
		undoOutcomeDownRow(),
		resendAfterRejectionRow(),
		graceWaitingRow(),
		graceRunningRow(),
		graceIntentRow(),
		graceLateOwnFieldRow(),
		graceSuccessCommittedRow(),
		graceReadLateFailureRow(),
		graceLateFailureRow(),
		callerDuringPredicateRow(),
		blockChainRow(),
		unknownThenUndoFailsRow(),
		lateCancelCompletesRow(),
		graceLateSuccessRow(),
		graceBlockedRow(),
		graceBlockAllRow(),
		graceTurnAfterReadRow(),
		graceBlockAllAfterReadRow(),
		blockAllBeforeGraceEndRow(),
		policyCopyRow(Continue, Block, stepCompensated),
		policyCopyRow(Block, Continue, stepBlocked),
		ownOutputRow(),
		sequenceRestoreRow(),
		failedStepInputRow(),
		callerValueRow(),
		continuePastFailedUndoRow(),
		unsentSuccessOutputRow(),
		levelWaitRow(),
		serialLevelRow(),
		compensableLevelRow(),
		emptyLevelRow(),
		graceLevelRow(),
		rejectedLevelRow(),
		branchNoWaitRow(),
	}
}

func (r *recorder) restorable(name string, hotel Hotel) *undoMakeStep {
	s := undoingMade(name, r.book(name, hotel), r.saw("undo:"+name))
	s.RetryPolicy = NoRetry()

	return s
}

func restoreChain(tail ...string) [][]string {
	return seq(append([]string{opened, startOf("a"), doneOf("a"), startOf("x"), failedOf("x")}, tail...)...)
}

func restoreRow(name string, steps func(rec *recorder) Flow) runRow {
	return runRow{
		name:            name,
		steps:           steps,
		wantStatus:      StatusCompensated,
		wantKind:        ErrCompensated,
		wantKinds:       map[string]*KindError{"x": ErrFailed},
		wantFailed:      []string{"x"},
		wantErrText:     map[string]string{"x": errAlwaysFails.Error()},
		wantAttempts:    map[string]int{"x": 1},
		wantErrs:        []error{errAlwaysFails},
		wantSteps:       map[string]stepStatus{"a": stepCompensated, "x": stepFailed},
		wantCompensated: []string{"a"},
		wantTrail:       restoreChain(compStartOf("a"), undoneOf("a"), closed),
	}
}

func ownOutputRow() runRow {
	row := restoreRow(
		"[Fail] When: a step with a restore compensation produced a field ; "+
			"Should: give the compensation that output, then restore the field",
		func(rec *recorder) Flow { return Sequence(rec.restorable("a", hotelRitz), once("x", alwaysFails)) },
	)
	row.wantOrder = [][2]string{{"a", "undo:a saw ritz"}}
	row.wantValues = map[string][]FieldValue{undoneOf("a"): nil}

	return row
}

func callerValueRow() runRow {
	row := restoreRow(
		"[Fail] When: the caller set a field and a step produced it ; "+
			"Should: restore the caller value",
		func(rec *recorder) Flow { return Sequence(rec.restorable("a", "x"), once("x", alwaysFails)) },
	)
	row.initial = tripState{Hotel: "mine"}
	row.wantOrder = [][2]string{{"a", "undo:a saw x"}}
	row.wantState = tripState{Hotel: "mine"}

	return row
}

func sequenceRestoreRow() runRow {
	return runRow{
		name: "[Fail] When: two steps in sequence produce one field and restore it ; " +
			"Should: give each compensation its own output and end on the initial value",
		wantCompensated: []string{"r1", "r2"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.restorable("r1", "r1"), rec.restorable("r2", "r2"), once("x", alwaysFails))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"x": ErrFailed},
		wantFailed:   []string{"x"},
		wantErrText:  map[string]string{"x": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"x": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps:    map[string]stepStatus{"r1": stepCompensated, "r2": stepCompensated, "x": stepFailed},
		wantOrder:    [][2]string{{"undo:r2 saw r2", "undo:r1 saw r1"}},
		wantTrail: seq(
			opened, startOf("r1"), doneOf("r1"), startOf("r2"), doneOf("r2"), startOf("x"), failedOf("x"),
			compStartOf("r2"), undoneOf("r2"), compStartOf("r1"), undoneOf("r1"), closed,
		),
		wantValues: map[string][]FieldValue{undoneOf("r2"): nil, undoneOf("r1"): nil},
	}
}

func failedStepInputRow() runRow {
	return runRow{
		name: "[Fail] When: the failing step has a restore compensation ; " +
			"Should: give it no output of its own and leave the field unchanged",
		wantCompensated: []string{"f"},
		steps: func(rec *recorder) Flow {
			failing := undoingMade("f", func(context.Context, tripState) (Hotel, error) {
				return "", errAlwaysFails
			}, rec.saw("undo:f"))
			failing.RetryPolicy = NoRetry()

			return Sequence(producing("a", rec.book("a", "a")), failing)
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"f": ErrFailed},
		wantFailed:   []string{"f"},
		wantErrText:  map[string]string{"f": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"f": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "f": stepCompensated},
		wantOrder:    [][2]string{{"a", "undo:f saw a"}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("f"), failedOf("f"), compStartOf("f"), undoneOf("f"), closed,
		),
		wantValues: map[string][]FieldValue{undoneOf("f"): nil},
		wantState:  tripState{Hotel: "a"},
	}
}

func continuePastFailedUndoRow() runRow {
	return runRow{
		name: "[Fail] When: a later compensation fails under Continue and an earlier restore succeeds ; " +
			"Should: keep the output of the failed compensation",
		wantCompensated: []string{"a1"},
		steps: func(rec *recorder) Flow {
			kept := undoingMade("a2", rec.book("a2", "a2"), alwaysFails)
			kept.RetryPolicy, kept.FailurePolicy = NoRetry(), Continue
			kept.RecoveryPolicy = RecoveryPolicy{Attempts: 1}

			return Sequence(rec.restorable("a1", "a1"), kept, once("x", alwaysFails))
		},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("a2"): ErrFailed, "x": ErrFailed},
		wantFailed:   []string{"x", comp("a2")},
		wantErrText:  map[string]string{comp("a2"): errAlwaysFails.Error(), "x": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("a2"): 1, "x": 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{ErrCompensated},
		wantSteps: map[string]stepStatus{
			"a1": stepCompensated, "a2": stepCompensationFailed, "x": stepFailed,
		},
		wantPending: []string{"a2"},
		wantOrder:   [][2]string{{"a2", "undo:a1 saw a1"}},
		wantTrail: seq(
			opened, startOf("a1"), doneOf("a1"), startOf("a2"), doneOf("a2"), startOf("x"), failedOf("x"),
			compStartOf("a2"), undoFailedOf("a2"), compStartOf("a1"), undoneOf("a1"), suspended,
		),
		wantState: tripState{Hotel: "a2"},
	}
}

func reversedOrderRow() runRow {
	return runRow{
		name: "[Fail] When: the last step of a chain fails ; " +
			"Should: compensate the chain in reverse, commit each undo",
		wantCompensated: []string{"a", "b"},
		steps: func(rec *recorder) Flow {
			b := replaceOnce("b", rec.book("b", hotelRitz), rec.book(undoB, refunded))

			return Sequence(rec.undoable("a"), b, once("c", alwaysFails))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "b": stepCompensated, "c": stepFailed},
		wantOrder:    [][2]string{{"b", undoB}, {undoB, undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), startOf("c"), failedOf("c"),
			compStartOf("b"), undoneOf("b"), compStartOf("a"), undoneOf("a"), closed,
		),
		wantValues: map[string][]FieldValue{undoneOf("b"): {hotelValue(refunded)}},
		wantState:  tripState{Hotel: refunded},
	}
}

func serialUndoRow() runRow {
	return runRow{
		name: "[Success] When: a serial run fails with three compensable parallel steps done ; " +
			"Should: compensation events arrive one at a time in reverse graph order",
		wantCompensated: []string{"a", "b", "c"},
		steps: func(rec *recorder) Flow {
			return Sequence(
				Parallel(rec.undoable("a"), rec.undoable("b"), rec.undoable("c")), once("x", alwaysFails),
			)
		},
		serial:       true,
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"x": ErrFailed},
		wantFailed:   []string{"x"},
		wantErrText:  map[string]string{"x": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"x": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"a": stepCompensated, "b": stepCompensated, "c": stepCompensated, "x": stepFailed,
		},
		wantOrder: [][2]string{{"undo:c", "undo:b"}, {"undo:b", undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), startOf("c"), doneOf("c"),
			startOf("x"), failedOf("x"), compStartOf("c"), undoneOf("c"), compStartOf("b"), undoneOf("b"),
			compStartOf("a"), undoneOf("a"), closed,
		),
	}
}

func failedAndUnknownRow() runRow {
	return runRow{
		name: "[Fail] When: one step fails and its sibling ends unknown ; " +
			"Should: compensate both, then the parent",
		wantCompensated: []string{"a", "b", "c"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), Parallel(
				undoOnce("b", alwaysFails, rec.do(undoB)),
				undoOnce("c", blockedOnCtx, rec.do(undoC)),
			))
		},
		wantStatus:    StatusCompensated,
		wantKind:      ErrCompensated,
		wantKinds:     map[string]*KindError{"b": ErrFailed, "c": ErrUnknown},
		wantFailed:    []string{"b", "c"},
		wantErrText:   map[string]string{"b": errAlwaysFails.Error(), "c": canceledTxt},
		wantAttempts:  map[string]int{"b": 1, "c": 1},
		wantCauseText: map[string]string{"c": stopText},
		wantErrs:      []error{errAlwaysFails, context.Canceled},
		wantSteps:     map[string]stepStatus{"a": stepCompensated, "b": stepCompensated, "c": stepCompensated},
		wantOrder:     [][2]string{{undoB, undoA}, {undoC, undoA}},
		wantTrail: afterA(
			together(startOf("b"), startOf("c"), failedB(), lostOf("c")),
			together(compStartOf("b"), undoneOf("b"), compStartOf("c"), undoneOf("c")),
			seq(compStartOf("a"), undoneOf("a"), closed),
		),
	}
}

func undoGoexitRow() runRow {
	return runRow{
		name: "[Fail] When: an Execute ends by runtime.Goexit ; " +
			"Should: compensate the unknown step and return",
		wantCompensated: []string{"a", "b"},
		steps: func(rec *recorder) Flow {
			leave := func(context.Context, tripState) error {
				runtime.Goexit()

				return nil
			}

			return Sequence(rec.undoable("a"), undoing("b", leave, rec.do(undoB)))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"b": ErrUnknown},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": goexitText},
		wantAttempts: map[string]int{"b": 1},
		wantErrs:     []error{errGoexit},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "b": stepCompensated},
		wantOrder:    [][2]string{{undoB, undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b"),
			compStartOf("b"), undoneOf("b"), compStartOf("a"), undoneOf("a"), closed,
		),
	}
}

func unknownBarrierRow() runRow {
	return runRow{
		name: "[Fail] When: a step before the pivot ends unknown ; " +
			"Should: cancel and wait for its sibling, compensate",
		wantCompensated: []string{"a", "b", "c"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), Parallel(
				undoOnce("b", panics, rec.do(undoB)),
				undoOnce("c", blockedOnCtx, rec.do(undoC)),
			), rec.pivot())
		},
		wantStatus:    StatusCompensated,
		wantKind:      ErrCompensated,
		wantKinds:     map[string]*KindError{"b": ErrPanic, "c": ErrUnknown},
		wantFailed:    []string{"b", "c"},
		wantErrText:   map[string]string{"b": panicBoom, "c": canceledTxt},
		wantAttempts:  map[string]int{"b": 1, "c": 1},
		wantCauseText: map[string]string{"c": stopText},
		wantErrs:      []error{context.Canceled},
		wantText:      []string{panicBoom},
		wantSteps: map[string]stepStatus{
			"a": stepCompensated, "b": stepCompensated, "c": stepCompensated, "p": stepNotStarted,
		},
		wantOrder: [][2]string{{undoB, undoA}, {undoC, undoA}},
		wantTrail: afterA(
			together(startOf("b"), startOf("c"), lostOf("b"), lostOf("c")),
			together(compStartOf("b"), undoneOf("b"), compStartOf("c"), undoneOf("c")),
			seq(compStartOf("a"), undoneOf("a"), closed),
		),
		wantZones: []StepZone{
			{Name: "a", Zone: ZoneCompensable, Compensable: true},
			{Name: "b", Zone: ZoneCompensable, Compensable: true},
			{Name: "c", Zone: ZoneCompensable, Compensable: true},
			{Name: "p", Zone: ZonePivot},
		},
	}
}

func siblingBarrierRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails beside a slow sibling ; " +
			"Should: compensate the sibling only after it returns",
		wantCompensated: []string{"a", "c"},
		steps: func(rec *recorder) Flow {
			slow := func(ctx context.Context, _ tripState) error {
				<-ctx.Done()
				time.Sleep(pause)
				rec.add(cReturned)

				return ctx.Err()
			}

			return Sequence(rec.undoable("a"), Parallel(once("b", alwaysFails), undoOnce("c", slow, rec.do(undoC))))
		},
		wantStatus:    StatusCompensated,
		wantKind:      ErrCompensated,
		wantKinds:     map[string]*KindError{"b": ErrFailed, "c": ErrUnknown},
		wantFailed:    []string{"b", "c"},
		wantErrText:   map[string]string{"b": errAlwaysFails.Error(), "c": canceledTxt},
		wantAttempts:  map[string]int{"b": 1, "c": 1},
		wantCauseText: map[string]string{"c": stopText},
		wantErrs:      []error{errAlwaysFails, context.Canceled},
		wantSteps:     map[string]stepStatus{"a": stepCompensated, "b": stepFailed, "c": stepCompensated},
		wantOrder:     [][2]string{{cReturned, undoC}, {undoC, undoA}},
		wantTrail: afterA(
			together(startOf("b"), startOf("c"), failedB(), lostOf("c")),
			seq(compStartOf("c"), undoneOf("c"), compStartOf("a"), undoneOf("a"), closed),
		),
	}
}

func callerDoneRow() runRow {
	return runRow{
		name: "[Fail] When: the caller's context is done before Run ; Should: run no step and return ErrCompensated",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"))
		},
		cancelCause: context.Canceled,
		wantStatus:  StatusCompensated,
		wantKind:    ErrCompensated,
		wantErrs:    []error{context.Canceled},
		wantSteps:   map[string]stepStatus{"a": stepNotStarted, "b": stepNotStarted},
		wantTrail:   seq(opened, closed),
	}
}

func callerDoneCauseRow() runRow {
	return runRow{
		name: "[Fail] When: the caller's context is done with a cause before Run ; " +
			"Should: match that cause and context.Canceled",
		steps: func(rec *recorder) Flow {
			return rec.undoable("a")
		},
		cancelCause: errStop,
		wantStatus:  StatusCompensated,
		wantKind:    ErrCompensated,
		wantErrs:    []error{errStop, context.Canceled},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepNotStarted},
		wantTrail:   seq(opened, closed),
	}
}

func loneFailureRow() runRow {
	return runRow{
		name: "[Fail] When: the only step fails and has no Compensate ; Should: end compensated with its Error",
		steps: func(*recorder) Flow {
			return once("a", alwaysFails)
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"a": ErrFailed},
		wantFailed:   []string{"a"},
		wantErrText:  map[string]string{"a": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"a": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps:    map[string]stepStatus{"a": stepFailed},
		wantTrail:    seq(opened, startOf("a"), failedOf("a"), closed),
	}
}

func permanentRow() runRow {
	return runRow{
		name: "[Fail] When: a NoRetry step returns a Permanent error ; Should: report 1 attempt in the Error and event",
		steps: func(*recorder) Flow {
			return once("a", func(context.Context, tripState) error { return Permanent(errBoom) })
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"a": ErrFailed},
		wantFailed:   []string{"a"},
		wantErrText:  map[string]string{"a": errBoom.Error()},
		wantAttempts: map[string]int{"a": 1},
		wantErrs:     []error{errBoom},
		wantSteps:    map[string]stepStatus{"a": stepFailed},
		wantTrail:    seq(opened, startOf("a"), failedOf("a"), closed),
	}
}

func predicateJournalRow() runRow {
	return runRow{
		name: "[Fail] When: an Enabled method panics and its failure append fails ; " +
			"Should: compensate the earlier step only and match ErrJournal",
		wantCompensated: []string{"a"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), panicGuarded(rec))
		},
		script:            map[int]error{4: errJournalDown},
		wantStatus:        StatusCompensated,
		wantKind:          ErrCompensated,
		wantKinds:         map[string]*KindError{"b": ErrPanic},
		wantFailed:        []string{"b"},
		wantErrText:       map[string]string{"b": panicBoom},
		wantAttempts:      map[string]int{"b": 0},
		wantErrs:          []error{ErrJournal, errJournalDown},
		wantEventAttempts: map[string]int{failedOf("b"): 0, doneOf("a"): 1},
		wantText:          []string{panicBoom},
		wantSteps:         map[string]stepStatus{"a": stepCompensated, "b": stepFailed},
		wantOrder:         [][2]string{{"a", undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), failedOf("b"), compStartOf("a"), undoneOf("a"), closed,
		),
	}
}

func predicateSiblingRow() runRow {
	return runRow{
		name: "[Fail] When: an Enabled method panics beside a compensable sibling ; " +
			"Should: compensate the sibling, not the panicking step",
		wantCompensated: []string{"a", "c"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), Parallel(panicGuarded(rec), undoOnce("c", blockedOnCtx, rec.do(undoC))))
		},
		wantStatus:    StatusCompensated,
		wantKind:      ErrCompensated,
		wantKinds:     map[string]*KindError{"b": ErrPanic, "c": ErrUnknown},
		wantFailed:    []string{"b", "c"},
		wantErrText:   map[string]string{"b": panicBoom, "c": canceledTxt},
		wantAttempts:  map[string]int{"b": 0, "c": 1},
		wantCauseText: map[string]string{"c": stopText},
		wantErrs:      []error{context.Canceled},
		wantText:      []string{panicBoom},
		wantSteps:     map[string]stepStatus{"a": stepCompensated, "b": stepFailed, "c": stepCompensated},
		wantOrder:     [][2]string{{undoC, undoA}},
		wantTrail: afterA(seq(
			startOf("c"), failedB(), lostOf("c"), compStartOf("c"), undoneOf("c"), compStartOf("a"), undoneOf("a"),
			closed,
		)),
	}
}

func policyRow(name string, policy FailurePolicy, a stepStatus, tail, pending []string) runRow {
	return runRow{
		name: name,
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), failingUndo("b", policy, alwaysFails), once("c", alwaysFails))
		},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrFailed, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 1, "c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{ErrCompensated},
		wantSteps:    map[string]stepStatus{"a": a, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:  pending,
		wantTrail:    chainTrail(slices.Concat([]string{compStartOf("b"), undoFailedOf("b")}, tail)...),
	}
}

func blockRow() runRow {
	return policyRow(
		"[Fail] When: a compensation with Block fails ; Should: block the earlier compensation",
		Block, stepBlocked, []string{blockedOf("a"), suspended}, []string{"a", "b"},
	)
}

func continueRow() runRow {
	row := policyRow(
		"[Fail] When: a compensation with Continue fails ; Should: still run the earlier compensation",
		Continue, stepCompensated, []string{compStartOf("a"), undoneOf("a"), suspended}, []string{"b"},
	)
	row.wantCompensated = []string{"a"}

	return row
}

func blockAllRow() runRow {
	return policyRow(
		"[Fail] When: a compensation with BlockAll fails ; Should: block the earlier compensation",
		BlockAll, stepBlocked, []string{blockedOf("a"), suspended}, []string{"a", "b"},
	)
}

func blockAcrossRow() runRow {
	return runRow{
		name: "[Fail] When: a blocked chain crosses a step without Compensate ; Should: block the step before it",
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoable(
					"a",
				),
				rec.step("x"),
				failingUndo("b", Block, alwaysFails),
				once("c", alwaysFails),
			)
		},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrFailed, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 1, "c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"a": stepBlocked, "x": stepSucceeded, "b": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"a", "b"},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("x"), doneOf("x"), startOf("b"), doneOf("b"),
			startOf("c"), failedOf("c"), compStartOf("b"), undoFailedOf("b"), blockedOf("a"), suspended,
		),
	}
}

func blockThenBlockAllRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation waits on a Block failure, then a later BlockAll failure ; Should: block it",
		steps: func(rec *recorder) Flow {
			late := func(context.Context, tripState) error {
				time.Sleep(pause)

				return errAlwaysFails
			}

			return Sequence(rec.undoable("a"), Parallel(
				failingUndo("b", Block, alwaysFails), failingUndo("d", BlockAll, late),
			), once("c", alwaysFails))
		},
		wantStatus: StatusCompensationFailed,
		wantKind:   ErrCompensationFailed,
		wantKinds:  map[string]*KindError{comp("b"): ErrFailed, "c": ErrFailed, comp("d"): ErrFailed},
		wantFailed: []string{"c", comp("b"), comp("d")},
		wantErrText: map[string]string{
			comp("b"): errAlwaysFails.Error(), "c": errAlwaysFails.Error(), comp("d"): errAlwaysFails.Error(),
		},
		wantAttempts: map[string]int{comp("b"): 1, "c": 1, comp("d"): 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"a": stepBlocked, "b": stepCompensationFailed, "c": stepFailed, "d": stepCompensationFailed,
		},
		wantPending: []string{"a", "b", "d"},
		wantTrail: afterA(
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d")),
			seq(undoFailedOf("b"), undoFailedOf("d"), blockedOf("a"), suspended),
		),
	}
}

func blockAllAtOnceRow() runRow {
	return runRow{
		name: "[Fail] When: a BlockAll failure happens while another compensation still runs ; " +
			"Should: block the waiting compensation at once",
		wantCompensated: []string{"b"},
		steps: func(rec *recorder) Flow {
			slow := func(context.Context, tripState) error {
				time.Sleep(pause)
				rec.add(undoB)

				return nil
			}

			return Sequence(rec.undoable("a"), Parallel(
				undoOnce("b", okAction, slow), failingUndo("d", BlockAll, alwaysFails),
			), once("c", alwaysFails))
		},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{"c": ErrFailed, comp("d"): ErrFailed},
		wantFailed:   []string{"c", comp("d")},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error(), comp("d"): errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1, comp("d"): 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"a": stepBlocked, "b": stepCompensated, "c": stepFailed, "d": stepCompensationFailed,
		},
		wantPending: []string{"a", "d"},
		wantTrail: afterA(
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d")),
			seq(undoFailedOf("d"), blockedOf("a"), undoneOf("b"), suspended),
		),
	}
}

func undoPanicRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation panics ; Should: fail it for good and apply its policy",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), failingUndo("b", Block, panics), once("c", alwaysFails))
		},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrPanic, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): panicBoom, "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 1, "c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantText:     []string{panicBoom},
		wantSteps:    map[string]stepStatus{"a": stepBlocked, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:  []string{"a", "b"},
		wantTrail:    chainTrail(compStartOf("b"), undoFailedOf("b"), blockedOf("a"), suspended),
	}
}

func lateRate(context.Context, tripState) (Rate, error) {
	time.Sleep(pause)

	return rateUndone, nil
}

func undoOwnFieldRow() runRow {
	return runRow{
		name: "[Fail] When: two parallel compensations replace their outputs ; " +
			"Should: each write only its own field",
		wantCompensated: []string{"b", "d"},
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				replaceOnce("b", rec.book("b", hotelRitz), rec.book(undoB, undoneHotel)),
				replaceRateOnce("d", makeOf(Rate(1)), lateRate),
			), once("c", alwaysFails))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps:    map[string]stepStatus{"b": stepCompensated, "c": stepFailed, "d": stepCompensated},
		wantTrail: slices.Concat(
			seq(opened),
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d"), undoneOf("b"), undoneOf("d")),
			seq(closed),
		),
		wantState: tripState{Hotel: undoneHotel, Rate: rateUndone},
	}
}

func undoIntentRow() runRow {
	return runRow{
		name: "[Fail] When: the intent append of a compensation fails ; Should: not run it and fail it with ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"), once("c", alwaysFails))
		},
		script:       map[int]error{8: errJournalDown},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrJournal, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): errJournalDown.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 0, "c": 1},
		wantErrs:     []error{ErrJournal, errJournalDown},
		wantSteps:    map[string]stepStatus{"a": stepBlocked, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:  []string{"a", "b"},
		wantTrail:    chainTrail(compStartOf("b"), undoFailedOf("b"), blockedOf("a"), suspended),
	}
}

func undoOutcomeRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a successful compensation fails once ; " +
			"Should: count it compensated once its resent success lands and go on with the next one",
		wantCompensated: []string{"a", "b"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"), once("c", alwaysFails))
		},
		script:       map[int]error{9: errJournalDown},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{ErrJournal, errJournalDown},
		wantNotErrs:  []error{ErrCompensationFailed},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "b": stepCompensated, "c": stepFailed},
		wantOrder:    [][2]string{{"b", undoB}, {undoB, undoA}},
		wantTrail:    chainTrail(compStartOf("b"), undoneOf("b"), compStartOf("a"), undoneOf("a"), closed),
	}
}

func undoOutcomeTwiceRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a successful compensation fails and its resend fails ; " +
			"Should: fail it with ErrJournal and append no failure after the success lands later",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"), once("c", alwaysFails))
		},
		script:          map[int]error{9: failing(errJournalDown, 2)},
		wantStatus:      StatusCompensationFailed,
		wantKind:        ErrCompensationFailed,
		wantKinds:       map[string]*KindError{comp("b"): ErrJournal, "c": ErrFailed},
		wantFailed:      []string{"c", comp("b")},
		wantErrText:     map[string]string{comp("b"): errJournalDown.Error(), "c": errAlwaysFails.Error()},
		wantAttempts:    map[string]int{comp("b"): 1, "c": 1},
		wantUnrecorded:  []string{comp("b")},
		wantErrs:        []error{ErrJournal, errJournalDown},
		wantSteps:       map[string]stepStatus{"a": stepBlocked, "b": stepCompensated, "c": stepFailed},
		wantCompensated: nil,
		wantPending:     []string{"a", "b"},
		wantRunOnly:     []string{"b"},
		wantOrder:       [][2]string{{"b", undoB}},
		wantTrail:       chainTrail(compStartOf("b"), undoneOf("b"), blockedOf("a"), suspended),
	}
}

func resendAfterRejectionRow() runRow {
	return runRow{
		name: "[Fail] When: the journal rejects the resend of a compensation success and a sibling compensation " +
			"fails later ; Should: append nothing after the rejection and end ErrConcurrentWriter",
		steps: func(rec *recorder) Flow {
			b := failingUndo("b", Block, func(context.Context, tripState) error {
				time.Sleep(pause)

				return errAlwaysFails
			})

			return Sequence(Parallel(b, rec.undoable("c")), once("d", alwaysFails))
		},
		script:     map[int]error{10: inTurn(errJournalDown, ErrConcurrentWriter)},
		wantStatus: noEndStatus,
		wantKind:   ErrConcurrentWriter,
		wantKinds:  map[string]*KindError{"d": ErrFailed, comp("c"): ErrJournal, comp("b"): ErrFailed},
		wantFailed: []string{"d", comp("b"), comp("c")},
		wantErrText: map[string]string{
			"d": errAlwaysFails.Error(), comp("c"): errJournalDown.Error(), comp("b"): errAlwaysFails.Error(),
		},
		wantAttempts:   map[string]int{"d": 1, comp("c"): 1, comp("b"): 1},
		wantUnrecorded: []string{comp("c"), comp("b")},
		wantErrs:       []error{ErrConcurrentWriter, ErrJournal, errJournalDown},
		wantSteps: map[string]stepStatus{
			"b": stepCompensationFailed, "c": stepCompensationFailed, "d": stepFailed,
		},
		wantPending: []string{"b", "c"},
		wantOrder:   [][2]string{{"c", undoC}},
		wantTrail: slices.Concat(
			seq(opened),
			together(startOf("b"), startOf("c"), doneOf("b"), doneOf("c")),
			seq(startOf("d"), failedOf("d")),
			together(compStartOf("b"), compStartOf("c")),
		),
	}
}

func undoOutcomeDownRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a successful compensation fails and the journal stays down ; " +
			"Should: fail it with ErrJournal and apply its policy",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"), once("c", alwaysFails))
		},
		script:         map[int]error{9: failing(errJournalDown, forever)},
		wantStatus:     noEndStatus,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{comp("b"): ErrJournal, "c": ErrFailed},
		wantFailed:     []string{"c", comp("b")},
		wantErrText:    map[string]string{comp("b"): errJournalDown.Error(), "c": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{comp("b"): 1, "c": 1},
		wantUnrecorded: []string{comp("b")},
		wantErrs:       []error{ErrJournal, errJournalDown},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:    []string{"a", "b"},
		wantRunOnly:    []string{"a"},
		wantOrder:      [][2]string{{"b", undoB}},
		wantTrail:      chainTrail(compStartOf("b")),
	}
}

func journalOutageRow() runRow {
	return runRow{
		name: "[Fail] When: the journal stays down from the first compensation intent on ; " +
			"Should: list every owed compensation in Pending",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"), once("c", alwaysFails))
		},
		script:         map[int]error{8: failing(errJournalDown, forever)},
		wantStatus:     noEndStatus,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{comp("b"): ErrJournal, "c": ErrFailed},
		wantFailed:     []string{"c", comp("b")},
		wantErrText:    map[string]string{comp("b"): errJournalDown.Error(), "c": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{comp("b"): 0, "c": 1},
		wantUnrecorded: []string{comp("b")},
		wantErrs:       []error{ErrJournal, errJournalDown},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded, "c": stepFailed},
		wantPending:    []string{"a", "b"},
		wantRunOnly:    []string{"a", "b"},
		wantTrail:      chainTrail(),
	}
}

func graceWaitingRow() runRow {
	return runRow{
		name: "[Fail] When: the grace period ends while a compensation waits for its turn ; " +
			"Should: fail it with ErrGraceEnded without running it",
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoable("a"), failingUndo("b", Continue, blockedOnCtx), once("c", alwaysFails),
			)
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  noEndStatus,
		wantKind:    ErrCompensationFailed,
		wantKinds:   map[string]*KindError{comp("a"): ErrGraceEnded, comp("b"): ErrUnknown, "c": ErrFailed},
		wantFailed:  []string{"c", comp("a"), comp("b")},
		wantErrText: map[string]string{
			comp("a"): ErrGraceEnded.Error(), comp("b"): canceledTxt, "c": errAlwaysFails.Error(),
		},
		wantAttempts:   map[string]int{comp("a"): 0, comp("b"): 1, "c": 1},
		wantCauseIs:    map[string]error{comp("b"): ErrGraceEnded},
		wantUnrecorded: []string{comp("b"), comp("a")},
		wantNotErrs:    []error{ErrJournal},
		wantErrs:       []error{ErrGraceEnded, errStop},
		wantSteps: map[string]stepStatus{
			"a": stepSucceeded, "b": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"a", "b"},
		wantRunOnly: []string{"a"},
		wantTrail:   chainTrail(compStartOf("b")),
	}
}

func graceRunningRow() runRow {
	return runRow{
		name: "[Fail] When: the grace period ends while a compensation runs ; Should: fail it with ErrGraceEnded",
		steps: func(*recorder) Flow {
			return Sequence(undoOnce("b", okAction, blockedOnCtx), once("c", alwaysFails))
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     noEndStatus,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{comp("b"): ErrUnknown, "c": ErrFailed},
		wantFailed:     []string{"c", comp("b")},
		wantErrText:    map[string]string{comp("b"): canceledTxt, "c": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{comp("b"): 1, "c": 1},
		wantCauseIs:    map[string]error{comp("b"): ErrGraceEnded},
		wantUnrecorded: []string{comp("b")},
		wantNotErrs:    []error{ErrJournal},
		wantErrs:       []error{ErrGraceEnded, errStop},
		wantTextCount:  map[string]int{ErrGraceEnded.Error(): 3},
		wantSteps:      map[string]stepStatus{"b": stepCompensationFailed, "c": stepFailed},
		wantPending:    []string{"b"},
		wantTrail: seq(
			opened, startOf("b"), doneOf("b"), startOf("c"), failedOf("c"), compStartOf("b"),
		),
	}
}

func graceLateOwnFieldRow() runRow {
	return runRow{
		name: "[Fail] When: a parallel compensation replaces its output after the grace period ; " +
			"Should: write only its own field and fail it with ErrGraceEnded",
		wantCompensated: []string{"b"},
		steps: func(rec *recorder) Flow {
			late := func(ctx context.Context, _ tripState) (Rate, error) {
				<-ctx.Done()

				return rateUndone, nil
			}

			return Sequence(Parallel(
				replaceOnce("b", rec.book("b", hotelRitz), rec.book(undoB, undoneHotel)),
				replaceRateOnce("d", makeOf(Rate(1)), late),
			), once("c", alwaysFails))
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     noEndStatus,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{"c": ErrFailed, comp("d"): ErrGraceEnded},
		wantFailed:     []string{"c", comp("d")},
		wantErrText:    map[string]string{"c": errAlwaysFails.Error(), comp("d"): ErrGraceEnded.Error()},
		wantAttempts:   map[string]int{"c": 1, comp("d"): 1},
		wantUnrecorded: []string{comp("d")},
		wantErrs:       []error{ErrGraceEnded, errStop},
		wantNotErrs:    []error{ErrCompensated, ErrJournal},
		wantTextCount:  map[string]int{ErrGraceEnded.Error(): 4},
		wantSteps:      map[string]stepStatus{"b": stepCompensated, "c": stepFailed, "d": stepCompensationFailed},
		wantPending:    []string{"d"},
		wantTrail: slices.Concat(
			seq(opened),
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d"), undoneOf("b")),
		),
		wantState: tripState{Hotel: undoneHotel, Rate: rateUndone},
	}
}

func slowUndo(rec *recorder, err error, policy FailurePolicy) *replaceStep {
	s := replaceOnce("b", makeOf[Hotel](""), func(context.Context, tripState) (Hotel, error) {
		time.Sleep(pause * 3 / 2)
		rec.add(undoB)

		return undoneHotel, err
	})
	s.FailurePolicy = policy

	return s
}

func readLateTrail(bOutcome, end string) [][]string {
	return slices.Concat(
		seq(opened),
		together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
		seq(startOf("c"), failedOf("c")),
		together(compStartOf("b"), compStartOf("d")),
		seq(undoneOf("d"), bOutcome, end),
	)
}

func graceSuccessCommittedRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation returns success before the grace end ; " +
			"Should: commit it as compensated and match neither the caller cause nor ErrGraceEnded",
		wantCompensated: []string{"b", "d"},
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				slowUndo(rec, nil, ""), undoOnce("d", okAction, rec.do("undo:d")),
			), once("c", alwaysFails))
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		script:       map[int]error{10: errAwaitThenStore},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{ErrGraceEnded, errStop},
		wantSteps:    map[string]stepStatus{"b": stepCompensated, "c": stepFailed, "d": stepCompensated},
		wantTrail:    readLateTrail(undoneOf("b"), closed),
		wantState:    tripState{Hotel: undoneHotel},
	}
}

func graceReadLateFailureRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation fails before the grace end and is read after it ; " +
			"Should: not join ErrGraceEnded to its error",
		wantCompensated: []string{"d"},
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				slowUndo(rec, errAlwaysFails, ""), undoOnce("d", okAction, rec.do("undo:d")),
			), once("c", alwaysFails))
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		script:       map[int]error{10: errAwaitThenStore},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{"c": ErrFailed, comp("b"): ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error(), comp("b"): errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1, comp("b"): 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{ErrGraceEnded, errStop},
		wantSteps:    map[string]stepStatus{"b": stepCompensationFailed, "c": stepFailed, "d": stepCompensated},
		wantPending:  []string{"b"},
		wantTrail:    readLateTrail(undoFailedOf("b"), suspended),
	}
}

func graceLateFailureRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation ignores its context and fails after the grace period ; " +
			"Should: keep ErrFailed and match ErrGraceEnded once",
		steps: func(*recorder) Flow {
			late := func(context.Context, tripState) error {
				time.Sleep(3 * pause)

				return errAlwaysFails
			}

			return Sequence(undoOnce("b", okAction, late), once("c", alwaysFails))
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     noEndStatus,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{comp("b"): ErrFailed, "c": ErrFailed},
		wantFailed:     []string{"c", comp("b")},
		wantErrText:    map[string]string{comp("b"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{comp("b"): 1, "c": 1},
		wantCauseIs:    map[string]error{comp("b"): ErrGraceEnded},
		wantUnrecorded: []string{comp("b")},
		wantNotErrs:    []error{ErrJournal},
		wantErrs:       []error{errAlwaysFails, ErrGraceEnded, errStop},
		wantTextCount:  map[string]int{ErrGraceEnded.Error(): 3},
		wantSteps:      map[string]stepStatus{"b": stepCompensationFailed, "c": stepFailed},
		wantPending:    []string{"b"},
		wantTrail: seq(
			opened, startOf("b"), doneOf("b"), startOf("c"), failedOf("c"), compStartOf("b"),
		),
	}
}

func graceIntentRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation intent append fails with the context error at the grace end ; " +
			"Should: join ErrGraceEnded to its error",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("b"), once("c", alwaysFails))
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		script:         map[int]error{6: errAwaitDone},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     noEndStatus,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{comp("b"): ErrGraceEnded, "c": ErrFailed},
		wantFailed:     []string{"c", comp("b")},
		wantErrText:    map[string]string{comp("b"): ErrGraceEnded.Error(), "c": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{comp("b"): 0, "c": 1},
		wantUnrecorded: []string{comp("b")},
		wantNotErrs:    []error{ErrJournal},
		wantErrs:       []error{ErrGraceEnded, context.Canceled, errStop},
		wantSteps:      map[string]stepStatus{"b": stepSucceeded, "c": stepFailed},
		wantPending:    []string{"b"},
		wantRunOnly:    []string{"b"},
		wantTrail:      seq(opened, startOf("b"), doneOf("b"), startOf("c"), failedOf("c")),
	}
}

func callerDuringPredicateRow() runRow {
	return runRow{
		name: "[Fail] When: the caller cancels while an Enabled method runs ; " +
			"Should: not start that step, compensate",
		wantCompensated: []string{"a"},
		steps: func(rec *recorder) Flow {
			slow := func(context.Context, tripState) bool {
				time.Sleep(pause)

				return true
			}

			return Sequence(rec.undoable("a"), rec.stepIf("b", slow))
		},
		cancelAfter: pause / 2,
		cancelCause: errStop,
		wantStatus:  StatusCompensated,
		wantKind:    ErrCompensated,
		wantErrs:    []error{errStop},
		wantSteps:   map[string]stepStatus{"a": stepCompensated, "b": stepNotStarted},
		wantOrder:   [][2]string{{"a", undoA}},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), compStartOf("a"), undoneOf("a"), closed),
	}
}

func blockChainRow() runRow {
	return runRow{
		name: "[Fail] When: a blocked compensation has a compensation before it ; Should: block that one too",
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoable("a"), rec.undoable("b"), failingUndo("c", Block, alwaysFails),
				once("d", alwaysFails),
			)
		},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("c"): ErrFailed, "d": ErrFailed},
		wantFailed:   []string{"d", comp("c")},
		wantErrText:  map[string]string{comp("c"): errAlwaysFails.Error(), "d": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("c"): 1, "d": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"a": stepBlocked, "b": stepBlocked, "c": stepCompensationFailed, "d": stepFailed,
		},
		wantPending: []string{"a", "b", "c"},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), startOf("c"), doneOf("c"),
			startOf("d"), failedOf("d"), compStartOf("c"), undoFailedOf("c"), blockedOf("b"), blockedOf("a"), suspended,
		),
	}
}

func unknownThenUndoFailsRow() runRow {
	return runRow{
		name: "[Fail] When: a step ends unknown and its compensation fails ; Should: report both errors in order",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), undoOnce("b", panics, alwaysFails))
		},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{"b": ErrPanic, comp("b"): ErrFailed},
		wantFailed:   []string{"b", comp("b")},
		wantErrText:  map[string]string{"b": panicBoom, comp("b"): errAlwaysFails.Error()},
		wantAttempts: map[string]int{"b": 1, comp("b"): 1},
		wantErrs:     []error{errAlwaysFails},
		wantText:     []string{panicBoom},
		wantSteps:    map[string]stepStatus{"a": stepBlocked, "b": stepCompensationFailed},
		wantPending:  []string{"a", "b"},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b"), compStartOf("b"), undoFailedOf("b"),
			blockedOf("a"), suspended,
		),
	}
}

func lateCancelCompletesRow() runRow {
	return runRow{
		name: "[Success] When: the caller cancels while the last step runs and it succeeds ; Should: complete",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), act("b", func(context.Context, tripState) error {
				time.Sleep(pause)

				return nil
			}))
		},
		cancelAfter: pause / 2,
		cancelCause: errStop,
		wantStatus:  StatusCompleted,
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), closed),
	}
}

func graceLateSuccessRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation succeeds only after the grace period ; " +
			"Should: commit it, and fail it with ErrGraceEnded when its outcome append is refused",
		steps: func(*recorder) Flow {
			late := func(context.Context, tripState) (Hotel, error) {
				time.Sleep(3 * pause)

				return undoneHotel, nil
			}

			return Sequence(replaceOnce("b", makeOf[Hotel](""), late), once("c", alwaysFails))
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     noEndStatus,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{"c": ErrFailed, comp("b"): ErrGraceEnded},
		wantFailed:     []string{"c", comp("b")},
		wantErrText:    map[string]string{"c": errAlwaysFails.Error(), comp("b"): ErrGraceEnded.Error()},
		wantAttempts:   map[string]int{"c": 1, comp("b"): 1},
		wantUnrecorded: []string{comp("b")},
		wantErrs:       []error{errAlwaysFails, ErrGraceEnded, errStop, context.Canceled},
		wantNotErrs:    []error{ErrCompensated, ErrJournal},
		wantSteps:      map[string]stepStatus{"b": stepCompensationFailed, "c": stepFailed},
		wantPending:    []string{"b"},
		wantTrail: seq(
			opened, startOf("b"), doneOf("b"), startOf("c"), failedOf("c"), compStartOf("b"),
		),
		wantState: tripState{Hotel: undoneHotel},
	}
}

func graceBlockedRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation is blocked below after the grace period ended ; " +
			"Should: count it blocked, not failed",
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoable("a"), failingUndo("b", Block, blockedOnCtx), once("c", alwaysFails),
			)
		},
		opts:           []Option{WithRecoveryTimeout(pause)},
		cancelAfter:    pause,
		cancelCause:    errStop,
		wantStatus:     noEndStatus,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{comp("b"): ErrUnknown, "c": ErrFailed},
		wantFailed:     []string{"c", comp("b")},
		wantErrText:    map[string]string{comp("b"): canceledTxt, "c": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{comp("b"): 1, "c": 1},
		wantCauseIs:    map[string]error{comp("b"): ErrGraceEnded},
		wantUnrecorded: []string{comp("b")},
		wantNotErrs:    []error{ErrJournal},
		wantErrs:       []error{ErrGraceEnded, errStop},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:    []string{"a", "b"},
		wantRunOnly:    []string{"a"},
		wantTrail:      chainTrail(compStartOf("b")),
	}
}

func graceBlockAllRow() runRow {
	return runRow{
		name: "[Fail] When: the grace period fails a waiting compensation with BlockAll ; " +
			"Should: apply BlockAll to the compensations before it",
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoable("x"), failingUndo("a", BlockAll, rec.do(undoA)),
				failingUndo("b", Continue, blockedOnCtx), once("c", alwaysFails),
			)
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  noEndStatus,
		wantKind:    ErrCompensationFailed,
		wantKinds:   map[string]*KindError{comp("a"): ErrGraceEnded, comp("b"): ErrUnknown, "c": ErrFailed},
		wantFailed:  []string{"c", comp("a"), comp("b")},
		wantErrText: map[string]string{
			comp("a"): ErrGraceEnded.Error(), comp("b"): canceledTxt, "c": errAlwaysFails.Error(),
		},
		wantAttempts:   map[string]int{comp("a"): 0, comp("b"): 1, "c": 1},
		wantCauseIs:    map[string]error{comp("b"): ErrGraceEnded},
		wantUnrecorded: []string{comp("b"), comp("a")},
		wantNotErrs:    []error{ErrJournal},
		wantErrs:       []error{ErrGraceEnded, errStop},
		wantSteps: map[string]stepStatus{
			"x": stepSucceeded, "a": stepSucceeded, "b": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"x", "a", "b"},
		wantRunOnly: []string{"x", "a"},
		wantTrail: seq(
			opened, startOf("x"), doneOf("x"), startOf("a"), doneOf("a"), startOf("b"), doneOf("b"),
			startOf("c"), failedOf("c"), compStartOf("b"),
		),
	}
}

func graceTurnAfterReadRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation's turn comes after the grace end and no call returned late ; " +
			"Should: fail it with ErrGraceEnded and match the caller cause",
		wantCompensated: []string{"b", "d"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), Parallel(
				slowUndo(rec, nil, ""), undoOnce("d", okAction, rec.do("undo:d")),
			), once("c", alwaysFails))
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		script:       map[int]error{12: errAwaitThenStore},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{"c": ErrFailed, comp("a"): ErrGraceEnded},
		wantFailed:   []string{"c", comp("a")},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error(), comp("a"): ErrGraceEnded.Error()},
		wantAttempts: map[string]int{"c": 1, comp("a"): 0},
		wantErrs:     []error{ErrGraceEnded, errStop},
		wantSteps: map[string]stepStatus{
			"a": stepCompensationFailed, "b": stepCompensated, "c": stepFailed, "d": stepCompensated,
		},
		wantPending: []string{"a"},
		wantTrail: afterA(
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d")),
			seq(undoneOf("d"), undoneOf("b"), undoFailedOf("a"), suspended),
		),
		wantState: tripState{Hotel: undoneHotel},
	}
}

func graceBlockAllAfterReadRow() runRow {
	return runRow{
		name: "[Fail] When: a BlockAll failure is read after the grace end ; " +
			"Should: block the waiting compensation and match the caller cause and context.Canceled",
		wantCompensated: []string{"d"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), Parallel(
				slowUndo(rec, errAlwaysFails, BlockAll),
				undoOnce("d", okAction, rec.do("undo:d")),
			), once("c", alwaysFails))
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		script:       map[int]error{12: errAwaitThenStore},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{"c": ErrFailed, comp("b"): ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error(), comp("b"): errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1, comp("b"): 1},
		wantErrs:     []error{errAlwaysFails, errStop, context.Canceled},
		wantNotErrs:  []error{ErrGraceEnded},
		wantSteps: map[string]stepStatus{
			"a": stepBlocked, "b": stepCompensationFailed, "c": stepFailed, "d": stepCompensated,
		},
		wantPending: []string{"a", "b"},
		wantTrail: afterA(
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d")),
			seq(undoneOf("d"), undoFailedOf("b"), blockedOf("a"), suspended),
		),
	}
}

func blockAllBeforeGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: a BlockAll failure blocks a compensation after a caller cancel, before the grace end ; " +
			"Should: not match the caller cause",
		steps: func(rec *recorder) Flow {
			slowFail := func(context.Context, tripState) error {
				time.Sleep(pause)

				return errAlwaysFails
			}

			return Sequence(rec.undoable("a"), failingUndo("b", BlockAll, slowFail), once("c", alwaysFails))
		},
		cancelAfter:  pause / 2,
		cancelCause:  errStop,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrFailed, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 1, "c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{errStop, ErrGraceEnded},
		wantSteps:    map[string]stepStatus{"a": stepBlocked, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:  []string{"a", "b"},
		wantTrail:    chainTrail(compStartOf("b"), undoFailedOf("b"), blockedOf("a"), suspended),
	}
}

func policyCopyRow(given, later FailurePolicy, a stepStatus) runRow {
	tail, pending, compensated := []string{compStartOf("a"), undoneOf("a"), suspended}, []string{"b"}, []string{"a"}
	if a == stepBlocked {
		tail, pending, compensated = []string{blockedOf("a"), suspended}, []string{"a", "b"}, nil
	}

	return runRow{
		name: fmt.Sprintf("[Fail] When: b has policy %s and Execute sets it to %s after New ; "+
			"Should: apply %s in every run", given, later, given),
		steps: func(rec *recorder) Flow {
			b := failingUndo("b", given, alwaysFails)
			c := once("c", func(context.Context, tripState) error {
				b.FailurePolicy = later

				return errAlwaysFails
			})

			return Sequence(rec.undoable("a"), b, c)
		},
		runs:            2,
		wantStatus:      StatusCompensationFailed,
		wantKind:        ErrCompensationFailed,
		wantKinds:       map[string]*KindError{comp("b"): ErrFailed, "c": ErrFailed},
		wantFailed:      []string{"c", comp("b")},
		wantErrText:     map[string]string{comp("b"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts:    map[string]int{comp("b"): 1, "c": 1},
		wantErrs:        []error{errAlwaysFails},
		wantSteps:       map[string]stepStatus{"a": a, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:     pending,
		wantCompensated: compensated,
		wantTrail:       chainTrail(slices.Concat([]string{compStartOf("b"), undoFailedOf("b")}, tail)...),
	}
}

func Test_decide(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		blockedAll   bool
		blockedBelow bool
		want         compDecision
	}{
		{name: "[Success] When: nothing failed before ; Should: run the compensation", want: compRun},
		{
			name:         "[Fail] When: a compensation it waits on is blocked ; Should: block it",
			blockedBelow: true,
			want:         compBlocked,
		},
		{
			name: "[Fail] When: a BlockAll failure happened and one it waits on is blocked ; " +
				"Should: block it under BlockAll",
			blockedAll:   true,
			blockedBelow: true,
			want:         compBlockedAll,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, decide(tt.blockedAll, tt.blockedBelow))
		})
	}
}

func Test_backwardDone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		children []int
		done     []bool
		want     bool
	}{
		{
			name: "[Success] When: the step has no children ; Should: be its turn",
			done: []bool{false},
			want: true,
		},
		{
			name:     "[Success] When: every child is backward-done ; Should: be its turn",
			children: []int{1, 2},
			done:     []bool{false, true, true},
			want:     true,
		},
		{
			name:     "[Fail] When: one child is not backward-done ; Should: not be its turn",
			children: []int{1, 2},
			done:     []bool{false, true, false},
		},
		{
			name:     "[Fail] When: the only child is not backward-done ; Should: not be its turn",
			children: []int{1},
			done:     []bool{false, false, false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, backwardDone(tt.children, tt.done))
		})
	}
}

func unsentSuccessOutputRow() runRow {
	return runRow{
		name: "[Fail] When: a producing step returns its output and its success append fails ; " +
			"Should: give the compensation that output and keep it when the compensation fails",
		steps: func(rec *recorder) Flow {
			a := undoingMade("a", rec.book("a", hotelRitz), func(ctx context.Context, s tripState) error {
				if err := rec.saw(undoA)(ctx, s); err != nil {
					return err
				}

				return errAlwaysFails
			})
			a.RetryPolicy, a.FailurePolicy = NoRetry(), Continue
			a.RecoveryPolicy = RecoveryPolicy{Attempts: 1}

			return a
		},
		script:       map[int]error{3: errJournalDown},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("a"): ErrFailed},
		wantFailed:   []string{comp("a")},
		wantErrText:  map[string]string{comp("a"): errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("a"): 1},
		wantErrs:     []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantSteps:    map[string]stepStatus{"a": stepCompensationFailed},
		wantPending:  []string{"a"},
		wantOrder:    [][2]string{{"a", "undo:a saw ritz"}},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), compStartOf("a"), undoFailedOf("a"), suspended),
		wantState:    tripState{Hotel: hotelRitz},
	}
}

func failsAfter(wait time.Duration) func(context.Context, tripState) error {
	return func(context.Context, tripState) error {
		time.Sleep(wait)

		return errAlwaysFails
	}
}

func (r *recorder) undoableAfter(name string, wait time.Duration) *undoStep {
	return undoOnce(name, r.do(name), func(ctx context.Context, s tripState) error {
		time.Sleep(wait)

		return r.do("undo:"+name)(ctx, s)
	})
}

func (r *recorder) undoableWith(name string, policy FailurePolicy) *undoStep {
	return failingUndo(name, policy, r.do("undo:"+name))
}

func levelWaitRow() runRow {
	return runRow{
		name: "[Success] When: BlockAll saga, a level-0 compensation fails while a level-0 sibling runs " +
			"and a level-1 step waits ; Should: the sibling ends compensated, the level-1 step is blocked",
		wantCompensated: []string{"z", "y"},
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				Sequence(rec.undoable("w"), rec.undoable("z")),
				failingUndo("x", BlockAll, failsAfter(pause)),
				rec.undoableAfter("y", 2*pause),
			), once("c", alwaysFails))
		},
		runs:         100,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("x"): ErrFailed, "c": ErrFailed},
		wantFailed:   []string{"c", comp("x")},
		wantErrText:  map[string]string{comp("x"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("x"): 1, "c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"w": stepBlocked, "z": stepCompensated, "x": stepCompensationFailed, "y": stepCompensated, "c": stepFailed,
		},
		wantPending: []string{"w", "x"},
		wantTrail: slices.Concat(
			seq(opened),
			together(
				startOf("w"), doneOf("w"), startOf("z"), doneOf("z"),
				startOf("x"), doneOf("x"), startOf("y"), doneOf("y"),
			),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("z"), compStartOf("x"), compStartOf("y")),
			seq(undoneOf("z"), undoFailedOf("x"), blockedOf("w"), undoneOf("y"), suspended),
		),
	}
}

func serialLevelRow() runRow {
	return runRow{
		name: "[Success] When: a serial run of a BlockAll saga reaches a level-1 step before a level-0 one ; " +
			"Should: run the level-0 compensations one at a time first and block the level-1 step",
		wantCompensated: []string{"z"},
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				failingUndo("x", BlockAll, alwaysFails), Sequence(rec.undoable("w"), rec.undoable("z")),
			), once("c", alwaysFails))
		},
		serial:       true,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("x"): ErrFailed, "c": ErrFailed},
		wantFailed:   []string{"c", comp("x")},
		wantErrText:  map[string]string{comp("x"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("x"): 1, "c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"w": stepBlocked, "z": stepCompensated, "x": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"x", "w"},
		wantTrail: seq(
			opened, startOf("x"), doneOf("x"), startOf("w"), doneOf("w"), startOf("z"), doneOf("z"),
			startOf("c"), failedOf("c"),
			compStartOf("z"), undoneOf("z"), compStartOf("x"), undoFailedOf("x"), blockedOf("w"), suspended,
		),
	}
}

func compensableLevelRow() runRow {
	return runRow{
		name: "[Success] When: BlockAll saga, P over N with no Compensate over M, and Q2 over Q over R, " +
			"and Q's compensation fails ; Should: P is compensated in level 1 beside Q, Q2 is blocked",
		wantCompensated: []string{"p", "m", "r"},
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				Sequence(rec.undoableWith("p", Block), rec.step("n"), rec.undoableAfter("m", pause)),
				Sequence(
					rec.undoableWith("q2", Block),
					failingUndo("q", BlockAll, alwaysFails),
					rec.undoableWith("r", Block),
				),
			), once("c", alwaysFails))
		},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("q"): ErrFailed, "c": ErrFailed},
		wantFailed:   []string{"c", comp("q")},
		wantErrText:  map[string]string{comp("q"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("q"): 1, "c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"p": stepCompensated, "n": stepSucceeded, "m": stepCompensated,
			"q2": stepBlocked, "q": stepCompensationFailed, "r": stepCompensated, "c": stepFailed,
		},
		wantPending: []string{"q2", "q"},
		wantTrail: slices.Concat(
			seq(opened),
			together(
				startOf("p"), doneOf("p"), startOf("n"), doneOf("n"), startOf("m"), doneOf("m"),
				startOf("q2"), doneOf("q2"), startOf("q"), doneOf("q"), startOf("r"), doneOf("r"),
			),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("m"), compStartOf("r")),
			seq(undoneOf("r"), undoneOf("m")),
			together(compStartOf("p"), compStartOf("q"), undoFailedOf("q"), blockedOf("q2"), undoneOf("p")),
			seq(suspended),
		),
	}
}

func emptyLevelRow() runRow {
	return runRow{
		name: "[Success] When: BlockAll saga with a level that holds no owed compensation ; " +
			"Should: the run ends with the next level compensated",
		wantCompensated: []string{"a"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoableWith("a", BlockAll), once("c", alwaysFails), rec.undoable("d"))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "c": stepFailed, "d": stepNotStarted},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("c"), failedOf("c"), compStartOf("a"), undoneOf("a"), closed,
		),
	}
}

func graceLevelRow() runRow {
	return runRow{
		name: "[Fail] When: BlockAll saga and the grace period ends while level 0 runs ; " +
			"Should: the later levels end with ErrGraceEnded and Run returns",
		wantCompensated: []string{"y"},
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				Sequence(
					rec.undoableWith("x", Continue), rec.undoableWith("a", Continue),
					failingUndo("b", Continue, blockedOnCtx),
				),
				rec.undoableWith("y", BlockAll),
			), once("c", alwaysFails))
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  noEndStatus,
		wantKind:    ErrCompensationFailed,
		wantKinds: map[string]*KindError{
			comp("x"): ErrGraceEnded, comp("a"): ErrGraceEnded, comp("b"): ErrUnknown, "c": ErrFailed,
		},
		wantFailed: []string{"c", comp("x"), comp("a"), comp("b")},
		wantErrText: map[string]string{
			comp("x"): ErrGraceEnded.Error(), comp("a"): ErrGraceEnded.Error(), comp("b"): canceledTxt,
			"c": errAlwaysFails.Error(),
		},
		wantAttempts:   map[string]int{comp("x"): 0, comp("a"): 0, comp("b"): 1, "c": 1},
		wantCauseIs:    map[string]error{comp("b"): ErrGraceEnded},
		wantUnrecorded: []string{comp("b"), comp("a"), comp("x")},
		wantNotErrs:    []error{ErrJournal},
		wantErrs:       []error{ErrGraceEnded, errStop},
		wantSteps: map[string]stepStatus{
			"x": stepSucceeded, "a": stepSucceeded, "b": stepCompensationFailed, "y": stepCompensated, "c": stepFailed,
		},
		wantPending: []string{"x", "a", "b"},
		wantRunOnly: []string{"x", "a"},
		wantTrail: slices.Concat(
			seq(opened),
			together(
				startOf("x"), doneOf("x"), startOf("a"), doneOf("a"), startOf("b"), doneOf("b"),
				startOf("y"), doneOf("y"),
			),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("y")),
			seq(undoneOf("y")),
		),
	}
}

func rejectedLevelRow() runRow {
	return runRow{
		name: "[Fail] When: BlockAll saga and the journal rejects the run during level 0 ; " +
			"Should: Run returns ErrConcurrentWriter and the loop ends",
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoableWith("a", BlockAll), Parallel(rec.undoableAfter("b", pause), rec.undoable("d")),
				once("c", alwaysFails),
			)
		},
		script:       map[int]error{12: ErrConcurrentWriter},
		wantStatus:   noEndStatus,
		wantKind:     ErrConcurrentWriter,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{ErrConcurrentWriter, errAlwaysFails},
		wantNotErrs:  []error{ErrJournal},
		wantSteps: map[string]stepStatus{
			"a": stepSucceeded, "b": stepCompensationFailed, "d": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"a", "b", "d"},
		wantRunOnly: []string{"a"},
		wantTrail: slices.Concat(
			seq(opened, startOf("a"), doneOf("a")),
			together(startOf("b"), doneOf("b"), startOf("d"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d")),
		),
	}
}

func branchNoWaitRow() runRow {
	return runRow{
		name: "[Success] When: Block saga with two independent branches and one stuck compensation ; " +
			"Should: the other branch compensates without waiting",
		wantCompensated: []string{"a", "b", "d", "e"},
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				Sequence(rec.undoable("a"), rec.undoableAfter("b", pause)),
				Sequence(rec.undoable("d"), rec.undoable("e")),
			), once("c", alwaysFails))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"a": stepCompensated, "b": stepCompensated, "d": stepCompensated, "e": stepCompensated, "c": stepFailed,
		},
		wantOrder: [][2]string{{"undo:d", undoB}},
		wantTrail: slices.Concat(
			seq(opened),
			together(
				startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), startOf("d"), doneOf("d"),
				startOf("e"), doneOf("e"),
			),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("e")),
			seq(undoneOf("e"), compStartOf("d"), undoneOf("d"), undoneOf("b"), compStartOf("a"), undoneOf("a"), closed),
		),
	}
}
