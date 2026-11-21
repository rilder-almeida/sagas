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
	endIncomplete
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
	status      runStatus
	started     bool
	uncommitted bool
	attempts    int
	err         *Error
}

type report interface {
	isReport()
}

type verdict struct {
	step  int
	run   bool
	label *KindError
	err   error
}

type settled[S any] struct {
	step   int
	out    outcome[S]
	base   snapshot[S]
	late   bool
	graced bool
}

func (verdict) isReport() {}

func (settled[S]) isReport() {}

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
	return len(r.failures) > 0 || r.rejected != nil || len(r.journalErrs) > 0 || r.interrupted != nil
}

func (r *runner[S]) record(ctx context.Context, e *Event) bool {
	return r.persist(ctx, e) == nil
}

func (r *runner[S]) persist(ctx context.Context, e *Event) error {
	if r.rejected != nil {
		return r.rejected
	}

	return r.append(ctx, e)
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
			cancel(r.stopReason)
		} else {
			r.launch(stepCtx, grace, c)
		}

		if c.running == 0 {
			break
		}

		r.receive(stepCtx, grace, c)
	}

	switch {
	case r.rejected != nil:
		return endRejected
	case !r.stopped():
		return endCompleted
	case r.pivotApplied():
		return endIncomplete
	default:
		return endFailed
	}
}

func (r *runner[S]) pivotApplied() bool {
	p := r.saga.pivot

	return p >= 0 && r.runs[p].started && r.runs[p].status != runFailed
}

func (r *runner[S]) callerStops(stepCtx, grace context.Context, i int) bool {
	if r.saga.steps[i].zone == ZoneForwardOnly && !graceEnded(grace) {
		return false
	}

	return r.callerDone(stepCtx)
}

func (r *runner[S]) callerDone(stepCtx context.Context) bool {
	if stepCtx.Err() == nil {
		return false
	}

	r.interrupted = callerCause(stepCtx)
	r.stop(r.interrupted)

	return true
}

func (r *runner[S]) launch(stepCtx, grace context.Context, c *crew[S]) {
	for _, i := range r.saga.order {
		if r.runs[i].status != runNotStarted || !ready(r.saga.steps[i].parents, r.runs) {
			continue
		}

		if r.callerStops(stepCtx, grace, i) {
			return
		}

		j := job[S]{step: i, base: r.store.snapshot()}

		switch {
		case r.saga.steps[i].pred != nil:
			j.answer = make(chan bool, 1)
			c.answers[i] = j.answer
		case !r.record(grace, &Event{Kind: EventStepStarted, Step: r.saga.steps[i].name}):
			return
		default:
			r.runs[i].started = true
		}

		r.runs[i].status = runRunning
		c.running++

		ctx := stepCtx
		if r.saga.steps[i].zone != ZoneCompensable {
			ctx = grace
		}

		c.wg.Go(func() { r.work(ctx, grace, c.reports, j) })
	}
}

func (r *runner[S]) receive(stepCtx, grace context.Context, c *crew[S]) {
	switch rep := (<-c.reports).(type) {
	case verdict:
		if r.answer(stepCtx, grace, rep, c.answers[rep.step]) {
			c.running--
		}
	case settled[S]:
		r.settle(grace, &rep)

		c.running--
	default:
		panic(fmt.Sprintf("sagas: forward received an unknown report of type %T", rep))
	}
}

func (r *runner[S]) work(ctx, grace context.Context, reports chan<- report, j job[S]) {
	if pred := r.saga.steps[j.step].pred; pred != nil {
		if !vet(pred, reports, j) || !<-j.answer {
			return
		}
	}

	r.act(ctx, j, r.saga.steps[j.step].action, func(done settled[S]) {
		done.late, done.graced = graceEnded(grace), r.saga.steps[j.step].zone != ZoneCompensable
		reports <- done
	})
}

func vet[S any](pred func(*S) bool, reports chan<- report, j job[S]) bool {
	v := verdict{step: j.step, err: errGoexit}

	defer func() { reports <- v }()

	v.run, v.label, v.err = judge(pred, j.base)

	return v.run && v.err == nil
}

func (r *runner[S]) act(ctx context.Context, j job[S], call callFunc[S], send func(settled[S])) {
	meta := &callMeta{execution: r.execution}
	done := settled[S]{step: j.step, base: j.base, out: outcome[S]{kind: outcomeUnknown, err: errGoexit}}
	returned := false

	defer func() {
		if !returned {
			done.out.attempts, done.out.previous = meta.attempt, meta.history()
		}

		send(done)
	}()

	done.out = call(ctx, meta, j.base)
	returned = true
}

func judge[S any](pred func(*S) bool, base snapshot[S]) (bool, *KindError, error) {
	view, err := copyState(base)
	if err != nil {
		return false, ErrInvalidState, err
	}

	var ok bool

	panicked, err := recoverPanic(func() error {
		ok = pred(view)

		return nil
	})

	if panicked {
		return false, ErrPanic, err
	}

	return ok, nil, nil
}

func (r *runner[S]) answer(stepCtx, grace context.Context, v verdict, reply chan<- bool) bool {
	i := v.step
	name := r.saga.steps[i].name

	switch {
	case r.rejected != nil:
		reply <- false

		if v.err != nil {
			r.note(i, outcome[S]{kind: outcomeFailure, err: v.err, label: v.label})
		}

		r.runs[i].status = runNotStarted
	case v.err != nil:
		reply <- false

		r.refuse(grace, i, outcome[S]{kind: outcomeFailure, err: v.err, label: v.label})
	case !v.run:
		reply <- false

		r.runs[i].status = runSkipped
		if !r.record(grace, &Event{Kind: EventStepSkipped, Step: name}) {
			r.runs[i].status = runNotStarted
		}
	case r.stopped() || r.callerStops(stepCtx, grace, i) ||
		!r.record(grace, &Event{Kind: EventStepStarted, Step: name}):
		reply <- false

		r.runs[i].status = runNotStarted
	default:
		reply <- true

		r.runs[i].started = true

		return false
	}

	return true
}

func (r *runner[S]) settle(grace context.Context, f *settled[S]) {
	i := f.step
	name := r.saga.steps[i].name

	if r.rejected != nil {
		if f.out.kind != outcomeSuccess {
			r.note(i, r.lateResult(f))
		}

		r.runs[i].status = runUnknown

		return
	}

	if f.out.kind != outcomeSuccess {
		r.fail(grace, i, r.lateResult(f))

		return
	}

	res, err := r.store.commit(name, f.base, *f.out.state)
	if err != nil {
		kind := outcomeFailure
		if i == r.saga.pivot {
			kind, r.runs[i].uncommitted = outcomeUnknown, true
		}

		r.fail(grace, i, outcome[S]{kind: kind, attempts: f.out.attempts, err: err, label: res.label})

		return
	}

	r.runs[i].status, r.runs[i].attempts = runSucceeded, f.out.attempts

	done := Event{Kind: EventStepSucceeded, Step: name, Attempt: f.out.attempts, Changed: res.changed}
	if !r.record(grace, &done) {
		r.runs[i].status = runUnknown
	}
}

func (r *runner[S]) fail(ctx context.Context, i int, out outcome[S]) {
	e := r.note(i, out)

	switch {
	case r.record(ctx, &e):
	case i == r.saga.pivot && out.kind == outcomeFailure:
		r.record(ctx, &e)
	default:
		r.runs[i].status = runUnknown
	}
}

func (r *runner[S]) refuse(ctx context.Context, i int, out outcome[S]) {
	e := r.note(i, out)
	r.record(ctx, &e)
}

func (r *runner[S]) note(i int, out outcome[S]) Event {
	name := r.saga.steps[i].name

	failure := &Error{
		Kind: kindOf(out.kind, out.label), Step: name, Attempts: out.attempts,
		Err: out.err, Cause: out.cause, Previous: out.previous,
	}
	e := Event{Kind: EventStepFailed, Step: name, Attempt: out.attempts, Err: eventErrorOf(failure)}
	r.runs[i].status, r.runs[i].attempts, r.runs[i].err = runFailed, out.attempts, failure
	r.failures = append(r.failures, failure)
	r.stop(fmt.Errorf("%w: step %q", errStepStopped, name))

	if out.kind == outcomeUnknown {
		e.Kind = EventStepUnknown
		r.runs[i].status = runUnknown
	}

	return e
}
