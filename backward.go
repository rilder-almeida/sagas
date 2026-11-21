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

func (r *runner[S]) turn(grace context.Context, b *rollback[S], i int, blockedBelow bool) {
	if errors.Is(context.Cause(grace), ErrGraceEnded) {
		r.lost(grace, b, i, outcome[S]{kind: outcomeFailure, err: ErrGraceEnded, label: ErrGraceEnded})

		return
	}

	switch decide(b.halted, blockedBelow) {
	case compHalted:
		r.skip(grace, b, i, EventCompensationHalted)
	case compBlocked:
		r.skip(grace, b, i, EventCompensationBlocked)
		b.tainted[i] = true
	case compRun:
		r.undo(grace, b, i)
	default:
	}
}

func (r *runner[S]) skip(grace context.Context, b *rollback[S], i int, kind EventKind) {
	b.done[i] = true
	r.record(grace, &Event{Kind: kind, Step: r.saga.steps[i].name})
}

func (r *runner[S]) undo(grace context.Context, b *rollback[S], i int) {
	step := &r.saga.steps[i]

	if err := r.persist(grace, &Event{Kind: EventCompensationStarted, Step: step.name}); err != nil {
		r.lost(grace, b, i, outcome[S]{kind: outcomeFailure, err: err, label: ErrJournal})

		return
	}

	j := job[S]{step: i, base: r.store.snapshot()}
	b.running++

	b.wg.Go(func() { r.act(grace, j, step.compensation, func(done settled[S]) { b.results <- done }) })
}

func (r *runner[S]) restore(grace context.Context, b *rollback[S], f *settled[S]) {
	name := r.saga.steps[f.step].name
	out := f.out

	if out.kind == outcomeSuccess {
		res, err := r.store.commit(name, f.base, *out.state)
		if err == nil {
			b.done[f.step] = true

			r.record(grace, &Event{
				Kind: EventCompensationSucceeded, Step: name, Attempt: out.attempts, Changed: res.changed,
			})

			return
		}

		out = outcome[S]{kind: outcomeFailure, attempts: out.attempts, err: err, label: res.label}
	}

	r.lost(grace, b, f.step, out)
}

func (r *runner[S]) lost(grace context.Context, b *rollback[S], i int, out outcome[S]) {
	name := r.saga.steps[i].name
	failure := &Error{
		Kind: kindOf(out.kind, out.label), Step: name, Compensation: true, Attempts: out.attempts,
		Err: out.err, Cause: out.cause, Previous: out.previous,
	}

	if errors.Is(context.Cause(grace), ErrGraceEnded) && !errors.Is(failure, ErrGraceEnded) {
		failure.Err = errors.Join(failure.Err, ErrGraceEnded)
	}

	b.done[i] = true

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
			r.skip(grace, b, i, EventCompensationHalted)
		}
	}
}
