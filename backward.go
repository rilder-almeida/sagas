package sagas

import (
	"context"
	"errors"
	"slices"
	"sync"
)

type compDecision int

const (
	compRun compDecision = iota
	compBlocked
	compHalted
)

type rollback[S any] struct {
	wg      sync.WaitGroup
	results chan settled[S]
	done    []bool
	begun   []bool
	tainted []bool
	halted  bool
	running int
}

func backwardDone(children []int, done []bool) bool {
	for _, c := range children {
		if !done[c] {
			return false
		}
	}

	return true
}

func decide(halted, blockedBelow bool) compDecision {
	switch {
	case halted:
		return compHalted
	case blockedBelow:
		return compBlocked
	default:
		return compRun
	}
}

func (r *runner[S]) backward(grace context.Context) {
	n := len(r.saga.steps)
	b := &rollback[S]{
		results: make(chan settled[S], n), done: make([]bool, n), begun: make([]bool, n), tainted: make([]bool, n),
	}

	defer b.wg.Wait()

	for {
		r.unwind(grace, b)

		if b.running == 0 {
			return
		}

		f := <-b.results
		b.running--

		r.restore(grace, b, &f)
	}
}

func (r *runner[S]) owes(i int) bool {
	return r.runs[i].started && r.saga.steps[i].compensation != nil
}

func (r *runner[S]) unwind(grace context.Context, b *rollback[S]) {
	for progressed := true; progressed; {
		progressed = false

		for _, i := range slices.Backward(r.saga.order) {
			children := r.saga.steps[i].children
			if b.done[i] || b.begun[i] || !backwardDone(children, b.done) {
				continue
			}

			progressed = true
			blockedBelow := slices.ContainsFunc(children, func(c int) bool { return b.tainted[c] })

			if !r.owes(i) {
				b.done[i], b.tainted[i] = true, blockedBelow

				continue
			}

			b.begun[i] = true
			r.turn(grace, b, i, blockedBelow)
		}
	}
}

func graceEnded(grace context.Context) bool {
	return errors.Is(context.Cause(grace), ErrGraceEnded)
}

func (r *runner[S]) turn(grace context.Context, b *rollback[S], i int, blockedBelow bool) {
	if r.rejected != nil {
		return
	}

	r.decidedUnrun(grace)

	switch decide(b.halted, blockedBelow) {
	case compHalted:
		r.skip(grace, b, i, EventCompensationHalted)
	case compBlocked:
		r.skip(grace, b, i, EventCompensationBlocked)
		b.tainted[i] = true
	case compRun:
		if graceEnded(grace) {
			r.lost(grace, b, i, outcome[S]{kind: outcomeFailure, err: ErrGraceEnded, label: ErrGraceEnded})

			return
		}

		r.undo(grace, b, i)
	default:
	}
}

func (r *runner[S]) skip(grace context.Context, b *rollback[S], i int, kind EventKind) {
	b.done[i], r.undone[i] = true, outcomeStatus(kind)
	r.record(grace, &Event{Kind: kind, Step: r.saga.steps[i].name})
}

func (r *runner[S]) undo(grace context.Context, b *rollback[S], i int) {
	step := &r.saga.steps[i]

	if err := r.persist(grace, &Event{Kind: EventCompensationStarted, Step: step.name}); err != nil {
		if r.rejected != nil {
			return
		}

		r.lost(grace, b, i, outcome[S]{kind: outcomeFailure, err: err, label: ErrJournal})

		return
	}

	j := job[S]{step: i, base: r.store.snapshot()}
	b.running++
	r.undone[i] = stepCompensationFailed

	b.wg.Go(func() {
		r.act(grace, j, step.compensation, func(done settled[S]) {
			done.late, done.graced = graceEnded(grace), true
			b.results <- done
		})
	})
}

func (r *runner[S]) restore(grace context.Context, b *rollback[S], f *settled[S]) {
	if f.out.kind == outcomeSuccess {
		if r.rejected != nil {
			return
		}

		out, ok := r.commitUndo(grace, f)
		if ok {
			b.done[f.step], r.undone[f.step] = true, stepCompensated

			return
		}

		if r.rejected != nil {
			return
		}

		f.out = out
	}

	r.lost(grace, b, f.step, r.lateResult(f))
}

func (r *runner[S]) commitUndo(grace context.Context, f *settled[S]) (outcome[S], bool) {
	name := r.saga.steps[f.step].name
	attempts := f.out.attempts

	res, err := r.store.commit(name, f.base, *f.out.state)
	if err != nil {
		return outcome[S]{kind: outcomeFailure, attempts: attempts, err: err, label: res.label}, false
	}

	done := Event{Kind: EventCompensationSucceeded, Step: name, Attempt: attempts, Changed: res.changed}
	if err = r.persist(grace, &done); err != nil {
		return outcome[S]{kind: outcomeFailure, attempts: attempts, err: err, label: ErrJournal}, false
	}

	return f.out, true
}

func (r *runner[S]) lateResult(f *settled[S]) outcome[S] {
	if !f.late || f.out.kind == outcomeSuccess {
		return f.out
	}

	r.graceHit = true

	if f.graced {
		return withGraceEnd(f.out)
	}

	return f.out
}

func (r *runner[S]) decidedUnrun(grace context.Context) {
	r.graceHit = r.graceHit || graceEnded(grace)
}

func withGraceEnd[S any](out outcome[S]) outcome[S] {
	own := errors.Join(append([]error{out.err, out.cause}, out.previous...)...)
	if errors.Is(own, ErrGraceEnded) {
		return out
	}

	out.err = errors.Join(out.err, ErrGraceEnded)

	return out
}

func (r *runner[S]) lost(grace context.Context, b *rollback[S], i int, out outcome[S]) {
	name := r.saga.steps[i].name
	failure := &Error{
		Kind: kindOf(out.kind, out.label), Step: name, Compensation: true, Attempts: out.attempts,
		Err: out.err, Cause: out.cause, Previous: out.previous,
	}

	b.done[i], r.undone[i] = true, stepCompensationFailed

	r.failures = append(r.failures, failure)
	r.record(grace, &Event{
		Kind: EventCompensationFailed, Step: name, Attempt: out.attempts, Err: eventErrorOf(failure),
	})

	policy := r.saga.steps[i].policy
	b.tainted[i] = policy == CompensationBlock

	if policy == CompensationHalt {
		r.halt(grace, b)
	}
}

func (r *runner[S]) halt(grace context.Context, b *rollback[S]) {
	b.halted = true

	for _, i := range slices.Backward(r.saga.order) {
		if !b.done[i] && !b.begun[i] && r.owes(i) {
			b.begun[i] = true

			r.decidedUnrun(grace)
			r.skip(grace, b, i, EventCompensationHalted)
		}
	}
}
