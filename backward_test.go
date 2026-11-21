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
	undoA       = "undo:a"
	undoB       = "undo:b"
	undoC       = "undo:c"
	cReturned   = "c returned"
	undoneHotel = "undo-b"
	canceledTxt = "context canceled"
)

type guardedUndoStep struct {
	Step
	doer
	undoer
	checker
}

func compStartOf(step string) string  { return trailOf(EventCompensationStarted, step) }
func undoneOf(step string) string     { return trailOf(EventCompensationSucceeded, step) }
func undoFailedOf(step string) string { return trailOf(EventCompensationFailed, step) }
func blockedOf(step string) string    { return trailOf(EventCompensationBlocked, step) }
func haltedOf(step string) string     { return trailOf(EventCompensationHalted, step) }

func failingUndo(
	name string,
	policy CompensationFailurePolicy,
	undo func(context.Context, *tripState) error,
) *undoStep {
	s := undoOnce(name, okAction, undo)
	s.CompensationFailure = policy

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

func panics(context.Context, *tripState) error { panic("boom") }

func panicking(*tripState) bool { panic("boom") }

func blockedOnCtx(ctx context.Context, _ *tripState) error {
	<-ctx.Done()

	return ctx.Err()
}

func undoOnce(name string, do, undo func(context.Context, *tripState) error) *undoStep {
	s := undoing(name, do, undo)
	s.Retry = NoRetry()

	return s
}

func backwardRows() []runRow {
	return []runRow{
		reversedOrderRow(),
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
		haltRow(),
		blockAcrossRow(),
		blockThenHaltRow(),
		haltAtOnceRow(),
		undoPanicRow(),
		undoConflictRow(),
		undoIntentRow(),
		journalOutageRow(),
		graceWaitingRow(),
		graceRunningRow(),
		graceIntentRow(),
		graceLateConflictRow(),
		graceReadLateSuccessRow(),
		graceReadLateFailureRow(),
		graceLateFailureRow(),
		callerDuringPredicateRow(),
		blockChainRow(),
		unknownThenUndoFailsRow(),
		lateCancelCompletesRow(),
		graceLateSuccessRow(),
		graceBlockedRow(),
		graceHaltRow(),
		graceTurnAfterReadRow(),
		graceHaltAfterReadRow(),
		haltBeforeGraceEndRow(),
		policyCopyRow(CompensationContinue, CompensationBlock, stepCompensated),
		policyCopyRow(CompensationBlock, CompensationContinue, stepBlocked),
	}
}

func reversedOrderRow() runRow {
	return runRow{
		name: "[Fail] When: the last step of a chain fails ; Should: compensate the chain in reverse, commit each undo",
		steps: func(rec *recorder) Flow {
			b := undoing("b", rec.book("b", hotelRitz), rec.book(undoB, refunded))

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
		wantState: tripState{Hotel: refunded},
	}
}

func failedAndUnknownRow() runRow {
	return runRow{
		name: "[Fail] When: one step fails and its sibling ends unknown ; Should: compensate both, then the parent",
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
		wantCauseText: map[string]string{"c": quotedB},
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
		name: "[Fail] When: an Execute ends by runtime.Goexit ; Should: compensate the unknown step and return",
		steps: func(rec *recorder) Flow {
			leave := func(context.Context, *tripState) error {
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
		name: "[Fail] When: a step before the pivot ends unknown ; Should: cancel and wait for its sibling, compensate",
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
		wantCauseText: map[string]string{"c": quotedB},
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
			{Name: "a", Zone: ZoneCompensable},
			{Name: "b", Zone: ZoneCompensable},
			{Name: "c", Zone: ZoneCompensable},
			{Name: "p", Zone: ZonePivot},
		},
	}
}

func siblingBarrierRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails beside a slow sibling ; Should: compensate the sibling only after it returns",
		steps: func(rec *recorder) Flow {
			slow := func(ctx context.Context, _ *tripState) error {
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
		wantCauseText: map[string]string{"c": quotedB},
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
		name: "[Fail] When: the caller's context is done with a cause before Run ; Should: match that cause",
		steps: func(rec *recorder) Flow {
			return rec.undoable("a")
		},
		cancelCause: errStop,
		wantStatus:  StatusCompensated,
		wantKind:    ErrCompensated,
		wantErrs:    []error{errStop},
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
			return once("a", func(context.Context, *tripState) error { return Permanent(errBoom) })
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
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), panicGuarded(rec))
		},
		script:         map[int]error{4: errJournalDown},
		wantStatus:     StatusCompensated,
		wantKind:       ErrCompensated,
		wantKinds:      map[string]*KindError{"b": ErrPanic},
		wantFailed:     []string{"b"},
		wantErrText:    map[string]string{"b": panicBoom},
		wantAttempts:   map[string]int{"b": 0},
		wantUnrecorded: []string{"b"},
		wantErrs:       []error{ErrJournal, errJournalDown},
		wantText:       []string{panicBoom},
		wantSteps:      map[string]stepStatus{"a": stepCompensated, "b": stepNotStarted},
		wantOrder:      [][2]string{{"a", undoA}},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), compStartOf("a"), undoneOf("a"), closed),
	}
}

func predicateSiblingRow() runRow {
	return runRow{
		name: "[Fail] When: an Enabled method panics beside a compensable sibling ; " +
			"Should: compensate the sibling, not the panicking step",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), Parallel(panicGuarded(rec), undoOnce("c", blockedOnCtx, rec.do(undoC))))
		},
		wantStatus:    StatusCompensated,
		wantKind:      ErrCompensated,
		wantKinds:     map[string]*KindError{"b": ErrPanic, "c": ErrUnknown},
		wantFailed:    []string{"b", "c"},
		wantErrText:   map[string]string{"b": panicBoom, "c": canceledTxt},
		wantAttempts:  map[string]int{"b": 0, "c": 1},
		wantCauseText: map[string]string{"c": quotedB},
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

func policyRow(name string, policy CompensationFailurePolicy, a stepStatus, tail, pending []string) runRow {
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
		"[Fail] When: a compensation with CompensationBlock fails ; Should: block the earlier compensation",
		CompensationBlock, stepBlocked, []string{blockedOf("a"), closed}, []string{"a", "b"},
	)
}

func continueRow() runRow {
	return policyRow(
		"[Fail] When: a compensation with CompensationContinue fails ; Should: still run the earlier compensation",
		CompensationContinue, stepCompensated, []string{compStartOf("a"), undoneOf("a"), closed}, []string{"b"},
	)
}

func haltRow() runRow {
	return policyRow(
		"[Fail] When: a compensation with CompensationHalt fails ; Should: halt the earlier compensation",
		CompensationHalt, stepHalted, []string{haltedOf("a"), closed}, []string{"a", "b"},
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
				failingUndo("b", CompensationBlock, alwaysFails),
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
			startOf("c"), failedOf("c"), compStartOf("b"), undoFailedOf("b"), blockedOf("a"), closed,
		),
	}
}

func blockThenHaltRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation waits on a Block failure, then a later Halt failure ; Should: halt it",
		steps: func(rec *recorder) Flow {
			late := func(context.Context, *tripState) error {
				time.Sleep(pause)

				return errAlwaysFails
			}

			return Sequence(rec.undoable("a"), Parallel(
				failingUndo("b", CompensationBlock, alwaysFails), failingUndo("d", CompensationHalt, late),
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
			"a": stepHalted, "b": stepCompensationFailed, "c": stepFailed, "d": stepCompensationFailed,
		},
		wantPending: []string{"a", "b", "d"},
		wantTrail: afterA(
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d")),
			seq(undoFailedOf("b"), undoFailedOf("d"), haltedOf("a"), closed),
		),
	}
}

func haltAtOnceRow() runRow {
	return runRow{
		name: "[Fail] When: a Halt failure happens while another compensation still runs ; " +
			"Should: halt the waiting compensation at once",
		steps: func(rec *recorder) Flow {
			slow := func(context.Context, *tripState) error {
				time.Sleep(pause)
				rec.add(undoB)

				return nil
			}

			return Sequence(rec.undoable("a"), Parallel(
				undoOnce("b", okAction, slow), failingUndo("d", CompensationHalt, alwaysFails),
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
			"a": stepHalted, "b": stepCompensated, "c": stepFailed, "d": stepCompensationFailed,
		},
		wantPending: []string{"a", "d"},
		wantTrail: afterA(
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d")),
			seq(undoFailedOf("d"), haltedOf("a"), undoneOf("b"), closed),
		),
	}
}

func undoPanicRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation panics ; Should: fail it for good and apply its policy",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), failingUndo("b", CompensationBlock, panics), once("c", alwaysFails))
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
		wantTrail:    chainTrail(compStartOf("b"), undoFailedOf("b"), blockedOf("a"), closed),
	}
}

func undoConflictRow() runRow {
	return runRow{
		name: "[Fail] When: two parallel compensations change the same field ; Should: fail the later one for good",
		steps: func(rec *recorder) Flow {
			late := func(_ context.Context, s *tripState) error {
				time.Sleep(pause)

				s.Hotel = "undo-d"

				return nil
			}

			return Sequence(Parallel(
				undoOnce("b", okAction, rec.book(undoB, undoneHotel)), undoOnce("d", okAction, late),
			), once("c", alwaysFails))
		},
		wantStatus: StatusCompensationFailed,
		wantKind:   ErrCompensationFailed,
		wantKinds:  map[string]*KindError{"c": ErrFailed, comp("d"): ErrConflict},
		wantFailed: []string{"c", comp("d")},
		wantErrText: map[string]string{
			"c": errAlwaysFails.Error(), comp("d"): `field Hotel changed by step "d" after step "b"`,
		},
		wantAttempts: map[string]int{"c": 1, comp("d"): 1},
		wantErrs:     []error{ErrConflict},
		wantSteps:    map[string]stepStatus{"b": stepCompensated, "c": stepFailed, "d": stepCompensationFailed},
		wantPending:  []string{"d"},
		wantTrail: slices.Concat(
			seq(opened),
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d"), undoneOf("b"), undoFailedOf("d")),
			seq(closed),
		),
		wantState: tripState{Hotel: undoneHotel},
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
		wantTrail:    chainTrail(undoFailedOf("b"), blockedOf("a"), closed),
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
		wantStatus:     StatusUnfinished,
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
				rec.undoable("a"), failingUndo("b", CompensationContinue, blockedOnCtx), once("c", alwaysFails),
			)
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  StatusCompensationFailed,
		wantKind:    ErrCompensationFailed,
		wantKinds:   map[string]*KindError{comp("a"): ErrGraceEnded, comp("b"): ErrUnknown, "c": ErrFailed},
		wantFailed:  []string{"c", comp("b"), comp("a")},
		wantErrText: map[string]string{
			comp("a"): ErrGraceEnded.Error(), comp("b"): canceledTxt, "c": errAlwaysFails.Error(),
		},
		wantAttempts: map[string]int{comp("a"): 0, comp("b"): 1, "c": 1},
		wantCauseIs:  map[string]error{comp("b"): ErrGraceEnded},
		wantErrs:     []error{ErrGraceEnded, errStop},
		wantSteps: map[string]stepStatus{
			"a": stepCompensationFailed, "b": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"a", "b"},
		wantTrail:   chainTrail(compStartOf("b"), undoFailedOf("b"), undoFailedOf("a"), closed),
	}
}

func graceRunningRow() runRow {
	return runRow{
		name: "[Fail] When: the grace period ends while a compensation runs ; Should: fail it with ErrGraceEnded",
		steps: func(*recorder) Flow {
			return Sequence(undoOnce("b", okAction, blockedOnCtx), once("c", alwaysFails))
		},
		opts:          []Option{WithRecoveryTimeout(pause)},
		cancelAfter:   pause,
		cancelCause:   errStop,
		wantStatus:    StatusCompensationFailed,
		wantKind:      ErrCompensationFailed,
		wantKinds:     map[string]*KindError{comp("b"): ErrUnknown, "c": ErrFailed},
		wantFailed:    []string{"c", comp("b")},
		wantErrText:   map[string]string{comp("b"): canceledTxt, "c": errAlwaysFails.Error()},
		wantAttempts:  map[string]int{comp("b"): 1, "c": 1},
		wantCauseIs:   map[string]error{comp("b"): ErrGraceEnded},
		wantErrs:      []error{ErrGraceEnded, errStop},
		wantTextCount: map[string]int{ErrGraceEnded.Error(): 1},
		wantSteps:     map[string]stepStatus{"b": stepCompensationFailed, "c": stepFailed},
		wantPending:   []string{"b"},
		wantTrail: seq(
			opened, startOf("b"), doneOf("b"), startOf("c"), failedOf("c"), compStartOf("b"), undoFailedOf("b"),
			closed,
		),
	}
}

func graceLateConflictRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation that would conflict returns after the grace period ; " +
			"Should: fail it with ErrGraceEnded, not ErrConflict",
		steps: func(rec *recorder) Flow {
			late := func(ctx context.Context, s *tripState) error {
				<-ctx.Done()

				s.Hotel = "undo-d"

				return nil
			}

			return Sequence(Parallel(
				undoOnce("b", okAction, rec.book(undoB, undoneHotel)), undoOnce("d", okAction, late),
			), once("c", alwaysFails))
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{"c": ErrFailed, comp("d"): ErrGraceEnded},
		wantFailed:   []string{"c", comp("d")},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error(), comp("d"): ErrGraceEnded.Error()},
		wantAttempts: map[string]int{"c": 1, comp("d"): 1},
		wantErrs:     []error{ErrGraceEnded, errStop},
		wantNotErrs:  []error{ErrConflict},
		wantSteps:    map[string]stepStatus{"b": stepCompensated, "c": stepFailed, "d": stepCompensationFailed},
		wantPending:  []string{"d"},
		wantTrail: slices.Concat(
			seq(opened),
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d"), undoneOf("b")),
			seq(undoFailedOf("d"), closed),
		),
		wantState: tripState{Hotel: undoneHotel},
	}
}

func slowUndo(rec *recorder, err error) func(context.Context, *tripState) error {
	return func(_ context.Context, s *tripState) error {
		time.Sleep(pause * 3 / 2)
		rec.add(undoB)

		if err != nil {
			return err
		}

		s.Hotel = undoneHotel

		return nil
	}
}

func readLateTrail(bOutcome string) [][]string {
	return slices.Concat(
		seq(opened),
		together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
		seq(startOf("c"), failedOf("c")),
		together(compStartOf("b"), compStartOf("d")),
		seq(undoneOf("d"), bOutcome, closed),
	)
}

func graceReadLateSuccessRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation succeeds before the grace end and is read after it ; " +
			"Should: commit it as compensated",
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				undoOnce("b", okAction, slowUndo(rec, nil)), undoOnce("d", okAction, rec.do("undo:d")),
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
		wantTrail:    readLateTrail(undoneOf("b")),
		wantState:    tripState{Hotel: undoneHotel},
	}
}

func graceReadLateFailureRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation fails before the grace end and is read after it ; " +
			"Should: not join ErrGraceEnded to its error",
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(
				undoOnce("b", okAction, slowUndo(rec, errAlwaysFails)), undoOnce("d", okAction, rec.do("undo:d")),
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
		wantTrail:    readLateTrail(undoFailedOf("b")),
	}
}

func graceLateFailureRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation ignores its context and fails after the grace period ; " +
			"Should: keep ErrFailed and match ErrGraceEnded once",
		steps: func(*recorder) Flow {
			late := func(context.Context, *tripState) error {
				time.Sleep(3 * pause)

				return errAlwaysFails
			}

			return Sequence(undoOnce("b", okAction, late), once("c", alwaysFails))
		},
		opts:          []Option{WithRecoveryTimeout(pause)},
		cancelAfter:   pause,
		cancelCause:   errStop,
		wantStatus:    StatusCompensationFailed,
		wantKind:      ErrCompensationFailed,
		wantKinds:     map[string]*KindError{comp("b"): ErrFailed, "c": ErrFailed},
		wantFailed:    []string{"c", comp("b")},
		wantErrText:   map[string]string{comp("b"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts:  map[string]int{comp("b"): 1, "c": 1},
		wantCauseIs:   map[string]error{comp("b"): ErrGraceEnded},
		wantErrs:      []error{errAlwaysFails, ErrGraceEnded, errStop},
		wantTextCount: map[string]int{ErrGraceEnded.Error(): 1},
		wantSteps:     map[string]stepStatus{"b": stepCompensationFailed, "c": stepFailed},
		wantPending:   []string{"b"},
		wantTrail: seq(
			opened, startOf("b"), doneOf("b"), startOf("c"), failedOf("c"), compStartOf("b"), undoFailedOf("b"),
			closed,
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
		opts:         []Option{WithRecoveryTimeout(pause)},
		script:       map[int]error{6: errAwaitDone},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrJournal, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): canceledTxt, "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 0, "c": 1},
		wantErrs:     []error{ErrJournal, ErrGraceEnded, context.Canceled, errStop},
		wantSteps:    map[string]stepStatus{"b": stepCompensationFailed, "c": stepFailed},
		wantPending:  []string{"b"},
		wantTrail:    seq(opened, startOf("b"), doneOf("b"), startOf("c"), failedOf("c"), undoFailedOf("b"), closed),
	}
}

func callerDuringPredicateRow() runRow {
	return runRow{
		name: "[Fail] When: the caller cancels while an Enabled method runs ; Should: not start that step, compensate",
		steps: func(rec *recorder) Flow {
			slow := func(*tripState) bool {
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
				rec.undoable("a"), rec.undoable("b"), failingUndo("c", CompensationBlock, alwaysFails),
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
			startOf("d"), failedOf("d"), compStartOf("c"), undoFailedOf("c"), blockedOf("b"), blockedOf("a"), closed,
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
			blockedOf("a"), closed,
		),
	}
}

func lateCancelCompletesRow() runRow {
	return runRow{
		name: "[Success] When: the caller cancels while the last step runs and it succeeds ; Should: complete",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), act("b", func(context.Context, *tripState) error {
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
			"Should: fail it with ErrGraceEnded and commit nothing",
		steps: func(*recorder) Flow {
			late := func(_ context.Context, s *tripState) error {
				time.Sleep(3 * pause)

				s.Hotel = undoneHotel

				return nil
			}

			return Sequence(undoOnce("b", okAction, late), once("c", alwaysFails))
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrGraceEnded, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): ErrGraceEnded.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 1, "c": 1},
		wantErrs:     []error{ErrGraceEnded, errStop},
		wantSteps:    map[string]stepStatus{"b": stepCompensationFailed, "c": stepFailed},
		wantPending:  []string{"b"},
		wantTrail: seq(
			opened, startOf("b"), doneOf("b"), startOf("c"), failedOf("c"), compStartOf("b"), undoFailedOf("b"),
			closed,
		),
	}
}

func graceBlockedRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation is blocked below after the grace period ended ; " +
			"Should: record it blocked, not failed",
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoable("a"), failingUndo("b", CompensationBlock, blockedOnCtx), once("c", alwaysFails),
			)
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrUnknown, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): canceledTxt, "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 1, "c": 1},
		wantCauseIs:  map[string]error{comp("b"): ErrGraceEnded},
		wantErrs:     []error{ErrGraceEnded, errStop},
		wantSteps:    map[string]stepStatus{"a": stepBlocked, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:  []string{"a", "b"},
		wantTrail:    chainTrail(compStartOf("b"), undoFailedOf("b"), blockedOf("a"), closed),
	}
}

func graceHaltRow() runRow {
	return runRow{
		name: "[Fail] When: the grace period fails a waiting compensation with CompensationHalt ; " +
			"Should: apply Halt to the compensations before it",
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoable("x"), failingUndo("a", CompensationHalt, rec.do(undoA)),
				failingUndo("b", CompensationContinue, blockedOnCtx), once("c", alwaysFails),
			)
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  StatusCompensationFailed,
		wantKind:    ErrCompensationFailed,
		wantKinds:   map[string]*KindError{comp("a"): ErrGraceEnded, comp("b"): ErrUnknown, "c": ErrFailed},
		wantFailed:  []string{"c", comp("b"), comp("a")},
		wantErrText: map[string]string{
			comp("a"): ErrGraceEnded.Error(), comp("b"): canceledTxt, "c": errAlwaysFails.Error(),
		},
		wantAttempts: map[string]int{comp("a"): 0, comp("b"): 1, "c": 1},
		wantCauseIs:  map[string]error{comp("b"): ErrGraceEnded},
		wantErrs:     []error{ErrGraceEnded, errStop},
		wantSteps: map[string]stepStatus{
			"x": stepHalted, "a": stepCompensationFailed, "b": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"x", "a", "b"},
		wantTrail: seq(
			opened, startOf("x"), doneOf("x"), startOf("a"), doneOf("a"), startOf("b"), doneOf("b"),
			startOf("c"), failedOf("c"), compStartOf("b"), undoFailedOf("b"), undoFailedOf("a"), haltedOf("x"),
			closed,
		),
	}
}

func graceTurnAfterReadRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation's turn comes after the grace end and no call returned late ; " +
			"Should: fail it with ErrGraceEnded and match the caller cause",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), Parallel(
				undoOnce("b", okAction, slowUndo(rec, nil)), undoOnce("d", okAction, rec.do("undo:d")),
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
			seq(undoneOf("d"), undoneOf("b"), undoFailedOf("a"), closed),
		),
		wantState: tripState{Hotel: undoneHotel},
	}
}

func graceHaltAfterReadRow() runRow {
	return runRow{
		name: "[Fail] When: a Halt failure is read after the grace end ; " +
			"Should: halt the waiting compensation and match the caller cause",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), Parallel(
				failingUndo("b", CompensationHalt, slowUndo(rec, errAlwaysFails)),
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
		wantErrs:     []error{errAlwaysFails, errStop},
		wantNotErrs:  []error{ErrGraceEnded},
		wantSteps: map[string]stepStatus{
			"a": stepHalted, "b": stepCompensationFailed, "c": stepFailed, "d": stepCompensated,
		},
		wantPending: []string{"a", "b"},
		wantTrail: afterA(
			together(startOf("b"), startOf("d"), doneOf("b"), doneOf("d")),
			seq(startOf("c"), failedOf("c")),
			together(compStartOf("b"), compStartOf("d")),
			seq(undoneOf("d"), undoFailedOf("b"), haltedOf("a"), closed),
		),
	}
}

func haltBeforeGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: a Halt failure halts a compensation after a caller cancel and before the grace end ; " +
			"Should: not match the caller cause",
		steps: func(rec *recorder) Flow {
			slowFail := func(context.Context, *tripState) error {
				time.Sleep(pause)

				return errAlwaysFails
			}

			return Sequence(rec.undoable("a"), failingUndo("b", CompensationHalt, slowFail), once("c", alwaysFails))
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
		wantSteps:    map[string]stepStatus{"a": stepHalted, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:  []string{"a", "b"},
		wantTrail:    chainTrail(compStartOf("b"), undoFailedOf("b"), haltedOf("a"), closed),
	}
}

func policyCopyRow(given, later CompensationFailurePolicy, a stepStatus) runRow {
	tail, pending := []string{compStartOf("a"), undoneOf("a"), closed}, []string{"b"}
	if a == stepBlocked {
		tail, pending = []string{blockedOf("a"), closed}, []string{"a", "b"}
	}

	return runRow{
		name: fmt.Sprintf("[Fail] When: b has policy %s and Execute sets it to %s after New ; "+
			"Should: apply %s in every run", given, later, given),
		steps: func(rec *recorder) Flow {
			b := failingUndo("b", given, alwaysFails)
			c := once("c", func(context.Context, *tripState) error {
				b.CompensationFailure = later

				return errAlwaysFails
			})

			return Sequence(rec.undoable("a"), b, c)
		},
		runs:         2,
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrFailed, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): errAlwaysFails.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 1, "c": 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps:    map[string]stepStatus{"a": a, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:  pending,
		wantTrail:    chainTrail(slices.Concat([]string{compStartOf("b"), undoFailedOf("b")}, tail)...),
	}
}

func Test_decide(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		halted       bool
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
			name:         "[Fail] When: a Halt failure happened and one it waits on is blocked ; Should: halt it",
			halted:       true,
			blockedBelow: true,
			want:         compHalted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, decide(tt.halted, tt.blockedBelow))
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
