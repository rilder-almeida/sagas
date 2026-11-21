package sagas

import (
	"context"
	"slices"
	"sync"
)

type rollback[S any] struct {
	wg      sync.WaitGroup
	results chan settled[S]
	done    []bool
	begun   []bool
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

func (r *runner[S]) backward(grace context.Context) {
	n := len(r.saga.steps)
	b := &rollback[S]{results: make(chan settled[S], n), done: make([]bool, n), begun: make([]bool, n)}

	defer b.wg.Wait()

	for {
		r.unwind(grace, b)

		if b.running == 0 {
			return
		}

		f := <-b.results
		b.running--

		r.restore(grace, &f)
		b.done[f.step] = true
	}
}

func (r *runner[S]) unwind(grace context.Context, b *rollback[S]) {
	for progressed := true; progressed; {
		progressed = false

		for _, i := range slices.Backward(r.saga.order) {
			if b.done[i] || b.begun[i] || !backwardDone(r.saga.steps[i].children, b.done) {
				continue
			}

			progressed = true

			if !r.runs[i].started || r.saga.steps[i].compensation == nil {
				b.done[i] = true

				continue
			}

			b.begun[i] = true
			r.undo(grace, b, i)
		}
	}
}

func (r *runner[S]) undo(grace context.Context, b *rollback[S], i int) {
	step := &r.saga.steps[i]

	if !r.record(grace, &Event{Kind: EventCompensationStarted, Step: step.name}) {
		b.done[i] = true

		return
	}

	j := job[S]{step: i, base: r.store.snapshot()}
	b.running++

	b.wg.Go(func() { r.act(grace, j, step.compensation, func(done settled[S]) { b.results <- done }) })
}

func (r *runner[S]) restore(grace context.Context, f *settled[S]) {
	name := r.saga.steps[f.step].name
	out := f.out

	if out.kind == outcomeSuccess {
		res, err := r.store.commit(name, f.base, *out.state)
		if err == nil {
			r.record(grace, &Event{
				Kind: EventCompensationSucceeded, Step: name, Attempt: out.attempts, Changed: res.changed,
			})

			return
		}

		out = outcome[S]{kind: outcomeFailure, attempts: out.attempts, err: err, label: res.label}
	}

	failure := &Error{
		Kind: kindOf(out.kind, out.label), Step: name, Compensation: true, Attempts: out.attempts,
		Err: out.err, Cause: out.cause, Previous: out.previous,
	}
	r.failures = append(r.failures, failure)
	r.record(grace, &Event{
		Kind: EventCompensationFailed, Step: name, Attempt: out.attempts, Err: eventErrorOf(failure),
	})
}
