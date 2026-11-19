package sagas

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const reportsPerStep = 2

var errGoexit = errors.New("sagas: call ended by runtime.Goexit")

type forwardEnd int

const (
	endCompleted forwardEnd = iota
	endFailed
	endRejected
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

type report any

type verdict struct {
	step int
	run  bool
	err  error
}

type settled[S any] struct {
	step int
	out  outcome[S]
	base snapshot[S]
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
	return len(r.failures) > 0 || r.rejected != nil || len(r.journalErrs) > 0
}

func (r *runner[S]) record(ctx context.Context, e *Event) bool {
	return r.rejected == nil && r.append(ctx, e) == nil
}

type crew[S any] struct {
	wg      sync.WaitGroup
	reports chan report
	answers []chan bool
	running int
}

func (r *runner[S]) forward(ctx, grace context.Context) forwardEnd {
	stepCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	c := &crew[S]{
		reports: make(chan report, reportsPerStep*len(r.saga.steps)),
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
	case r.rejected != nil:
		return endRejected
	case len(r.failures) > 0 || len(r.journalErrs) > 0:
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
	switch rep := (<-c.reports).(type) {
	case verdict:
		if r.decide(grace, rep, c.answers[rep.step]) {
			c.running--
		}
	case settled[S]:
		r.settle(grace, &rep)

		c.running--
	default:
		panic(fmt.Sprintf("sagas: forward received an unknown report of type %T", rep))
	}
}

func (r *runner[S]) work(ctx context.Context, reports chan<- report, j job[S]) {
	if pred := r.saga.steps[j.step].pred; pred != nil {
		if !vet(pred, reports, j) || !<-j.answer {
			return
		}
	}

	r.act(ctx, reports, j)
}

func vet[S any](pred func(*S) bool, reports chan<- report, j job[S]) bool {
	v := verdict{step: j.step, err: errGoexit}

	defer func() { reports <- v }()

	v.run, v.err = judge(pred, j.base)

	return v.run && v.err == nil
}

func (r *runner[S]) act(ctx context.Context, reports chan<- report, j job[S]) {
	done := settled[S]{step: j.step, base: j.base, out: outcome[S]{kind: outcomeUnknown, err: errGoexit}}

	defer func() { reports <- done }()

	done.out = r.saga.steps[j.step].action(ctx, r.execution, j.base)
}

func judge[S any](pred func(*S) bool, base snapshot[S]) (bool, error) {
	view, err := copyState(base)
	if err != nil {
		return false, err
	}

	var ok bool

	err = catchPanic(func() error {
		ok = pred(view)

		return nil
	})

	return ok && err == nil, err
}

func (r *runner[S]) decide(grace context.Context, v verdict, answer chan<- bool) bool {
	i := v.step
	name := r.saga.steps[i].name

	switch {
	case r.rejected != nil:
		answer <- false

		r.runs[i].status = runNotStarted
	case v.err != nil:
		answer <- false

		r.fail(grace, i, outcome[S]{kind: outcomeFailure, err: v.err})
	case !v.run:
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

func (r *runner[S]) settle(grace context.Context, f *settled[S]) {
	i := f.step
	name := r.saga.steps[i].name

	if r.rejected != nil {
		r.runs[i].status = runUnknown

		return
	}

	if f.out.kind != outcomeSuccess {
		r.fail(grace, i, f.out)

		return
	}

	changed, err := r.store.commit(name, f.base, *f.out.state)
	if err != nil {
		r.fail(grace, i, outcome[S]{kind: outcomeFailure, attempts: f.out.attempts, err: err})

		return
	}

	r.runs[i].status, r.runs[i].attempts = runSucceeded, f.out.attempts

	done := Event{Kind: EventStepSucceeded, Step: name, Attempt: f.out.attempts, Changed: changed}
	if !r.record(grace, &done) {
		r.runs[i].status = runUnknown
	}
}

func (r *runner[S]) fail(ctx context.Context, i int, out outcome[S]) {
	name := r.saga.steps[i].name
	failure := &StepError{Step: name, Attempts: out.attempts, Err: out.err}
	e := Event{Kind: EventStepFailed, Step: name, Attempt: out.attempts, Err: out.err.Error()}
	r.runs[i] = stepRun{status: runFailed, attempts: out.attempts, err: failure}
	r.failures = append(r.failures, failure)

	if out.kind == outcomeUnknown {
		e.Kind = EventStepUnknown
		r.runs[i].status = runUnknown
	}

	if !r.record(ctx, &e) {
		r.runs[i].status = runUnknown
	}
}
