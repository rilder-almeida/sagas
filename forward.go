package sagas

import (
	"context"
	"errors"
	"sync"
)

const reportsPerStep = 2

var errGoexit = errors.New("sagas: call ended by runtime.Goexit")

type forwardEnd int

const (
	endCompleted forwardEnd = iota
	endFailed
	endAborted
)

type runStatus int

const (
	runNotStarted runStatus = iota
	runRunning
	runSucceeded
	runFailed
	runUnknown
	runSkipped
)

type stepRun struct {
	status   runStatus
	attempts int
	err      *StepError
}

type report[S any] struct {
	step      int
	predicate bool
	run       bool
	out       outcome[S]
	base      snapshot[S]
}

type job[S any] struct {
	step   int
	base   snapshot[S]
	answer chan bool
}

func ready(parents []int, status []stepRun) bool {
	for _, p := range parents {
		if s := status[p].status; s != runSucceeded && s != runSkipped {
			return false
		}
	}

	return true
}

func (r *runner[S]) stopped() bool {
	return r.failure != nil || r.rejected != nil || len(r.journalErrs) > 0
}

func (r *runner[S]) record(ctx context.Context, e *Event) bool {
	return r.append(ctx, e) == nil
}

type crew[S any] struct {
	wg      sync.WaitGroup
	reports chan report[S]
	answers []chan bool
	running int
}

func (r *runner[S]) forward(ctx, grace context.Context) forwardEnd {
	stepCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	c := &crew[S]{
		reports: make(chan report[S], reportsPerStep*len(r.saga.steps)),
		answers: make([]chan bool, len(r.saga.steps)),
	}
	defer c.wg.Wait()

	for {
		if r.stopped() {
			cancel(errors.Join(r.errs()...))
		} else {
			r.launch(stepCtx, grace, c)
		}

		if c.running == 0 {
			break
		}

		r.receive(grace, c)
	}

	switch {
	case r.rejected != nil || len(r.journalErrs) > 0:
		return endAborted
	case r.failure != nil:
		return endFailed
	default:
		return endCompleted
	}
}

func (r *runner[S]) launch(stepCtx, grace context.Context, c *crew[S]) {
	for _, i := range r.saga.order {
		if r.runs[i].status != runNotStarted || !ready(r.saga.steps[i].parents, r.runs) {
			continue
		}

		j := job[S]{step: i, base: r.store.snapshot()}

		if r.saga.steps[i].pred != nil {
			j.answer = make(chan bool, 1)
			c.answers[i] = j.answer
		} else if !r.record(grace, &Event{Kind: EventStepStarted, Step: r.saga.steps[i].name}) {
			return
		}

		r.runs[i].status = runRunning
		c.running++

		c.wg.Go(func() { r.work(stepCtx, c.reports, j) })
	}
}

func (r *runner[S]) receive(grace context.Context, c *crew[S]) {
	rep := <-c.reports

	finished := true
	if rep.predicate {
		finished = r.decide(grace, &rep, c.answers[rep.step])
	} else {
		r.settle(grace, &rep)
	}

	if finished {
		c.running--
	}
}

func (r *runner[S]) work(ctx context.Context, reports chan<- report[S], j job[S]) {
	node := &r.saga.steps[j.step]
	final := report[S]{step: j.step, base: j.base, out: outcome[S]{kind: outcomeUnknown, err: errGoexit}}
	quiet := false

	defer func() {
		if !quiet {
			reports <- final
		}
	}()

	if node.pred != nil {
		final.predicate, final.out.kind = true, outcomeFailure

		verdict := report[S]{step: j.step, predicate: true}
		verdict.run, verdict.out.err = judge(node.pred, j.base)

		if verdict.out.err != nil {
			verdict.out.kind = outcomeFailure
		}

		quiet = true

		reports <- verdict

		if !<-j.answer {
			return
		}

		quiet = false
		final.predicate, final.out.kind = false, outcomeUnknown
	}

	final.out = node.action(ctx, r.execution, j.base)
}

func judge[S any](pred func(*S) bool, base snapshot[S]) (bool, error) {
	view, err := copyState(base)
	if err != nil {
		return false, err
	}

	var verdict bool

	err = catchPanic(func() error {
		verdict = pred(view)

		return nil
	})

	return verdict && err == nil, err
}

func (r *runner[S]) decide(grace context.Context, rep *report[S], answer chan<- bool) bool {
	i := rep.step
	name := r.saga.steps[i].name

	switch {
	case rep.out.kind == outcomeFailure:
		answer <- false

		r.fail(grace, i, rep.out)
	case !rep.run:
		answer <- false

		r.runs[i].status = runSkipped
		r.record(grace, &Event{Kind: EventStepSkipped, Step: name})
	case r.stopped() || !r.record(grace, &Event{Kind: EventStepStarted, Step: name}):
		answer <- false

		r.runs[i].status = runNotStarted
	default:
		answer <- true

		return false
	}

	return true
}

func (r *runner[S]) settle(grace context.Context, rep *report[S]) {
	i := rep.step
	out := rep.out

	if out.kind != outcomeSuccess {
		r.fail(grace, i, out)

		return
	}

	changed, err := r.store.commit(r.saga.steps[i].name, rep.base, *out.state)
	if err != nil {
		out.kind, out.err = outcomeFailure, err
		r.fail(grace, i, out)

		return
	}

	r.runs[i].status, r.runs[i].attempts = runSucceeded, out.attempts

	done := Event{Kind: EventStepSucceeded, Step: r.saga.steps[i].name, Attempt: out.attempts, Changed: changed}
	if !r.record(grace, &done) {
		r.runs[i].status = runUnknown
	}
}

func (r *runner[S]) fail(ctx context.Context, i int, out outcome[S]) {
	name := r.saga.steps[i].name
	failure := &StepError{Step: name, Attempts: out.attempts, Err: out.err}
	e := Event{Kind: EventStepFailed, Step: name, Attempt: out.attempts, Err: out.err.Error()}
	r.runs[i] = stepRun{status: runFailed, attempts: out.attempts, err: failure}

	if out.kind == outcomeUnknown {
		e.Kind = EventStepUnknown
		r.runs[i].status = runUnknown
	}

	if r.failure == nil {
		r.failure = failure
	}

	r.record(ctx, &e)
}
