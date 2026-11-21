package sagas

import (
	"context"
	"runtime"
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
	canceledTxt = "context canceled"
)

type guardedUndoStep struct {
	Step
	doer
	undoer
	checker
}

func compStartOf(step string) string { return trailOf(EventCompensationStarted, step) }
func undoneOf(step string) string    { return trailOf(EventCompensationSucceeded, step) }

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
			), rec.pivot("p"))
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
			name:     "[Fail] When: a child without Compensate waits on its own child ; Should: not be its turn",
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
