package sagas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// ErrInvalidRun is returned by Run for an execution ID that breaks a rule of the run input.
var ErrInvalidRun = &KindError{name: "invalid_run", message: "sagas: invalid run"}

const idBytes = 16

type executionIDer interface{ ExecutionID() string }

func checkRun[S any](s *S) (string, error) {
	if s == nil {
		return "", fmt.Errorf("%w: state is nil", ErrInvalidState)
	}

	execution := newID()
	if given, ok := any(s).(executionIDer); ok {
		execution = given.ExecutionID()
	}

	var errs []error
	for _, rule := range checkName(execution) {
		errs = append(errs, fmt.Errorf("%w: %s", ErrInvalidRun, rule))
	}

	if _, err := encodeFields(reflect.ValueOf(*s)); err != nil {
		errs = append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		return "", err
	}

	return execution, nil
}

func newID() string {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("sagas: crypto/rand failed: %v", err))
	}

	return hex.EncodeToString(b)
}

type runner[S any] struct {
	saga        *Saga[S]
	execution   string
	writer      string
	journal     Journal
	seq         int
	events      []Event
	unsent      *Event
	unsentErr   error
	appendErrs  []error
	rejected    error
	interrupted error
	graceHit    bool
	stopReason  error
	store       *store[S]
	runs        []stepRun
	undone      []stepStatus
	failures    []*Error
}

func (r *runner[S]) append(ctx context.Context, e *Event) error {
	if err := r.resend(ctx); err != nil {
		return err
	}

	stored := r.stamp(e)

	return r.send(ctx, &stored)
}

func (r *runner[S]) stamp(e *Event) Event {
	stored := *e
	stored.Saga = r.saga.name
	stored.Execution = r.execution
	stored.Writer = r.writer
	stored.Seq = r.seq + 1
	stored.Version = EventVersion
	stored.Time = time.Now()
	cloneSlices(&stored)

	return stored
}

func (r *runner[S]) resend(ctx context.Context) error {
	e, cause := r.unsent, r.unsentErr
	if e == nil {
		return nil
	}

	if r.rejected != nil {
		return r.rejected
	}

	if err := r.send(ctx, e); err != nil {
		return err
	}

	if e.Kind != EventStepStarted {
		return nil
	}

	failure := &Error{Kind: appendLabel(cause), Step: e.Step, Err: cause}
	closing := r.stamp(&Event{Kind: EventStepFailed, Step: e.Step, Err: eventErrorOf(failure)})

	return r.send(ctx, &closing)
}

func (r *runner[S]) send(ctx context.Context, e *Event) error {
	if err := r.journal.Append(ctx, e); err != nil {
		if errors.Is(err, ErrExecutionExists) || errors.Is(err, ErrConcurrentWriter) {
			r.rejected = err
			r.stop(errRejectionStopped)

			return err
		}

		late := e.Kind != EventSagaStarted && graceEnded(ctx)

		reason := fmt.Errorf("%w: %w", ErrJournal, err)
		if late && ownError(ctx, err) {
			reason = ErrGraceEnded
		}

		failed := settled[S]{out: outcome[S]{kind: outcomeFailure, err: reason}, late: late, graced: true}
		err = r.lateResult(&failed).err
		r.appendErrs = append(r.appendErrs, err)

		if r.unsent != e {
			r.unsent, r.unsentErr = e, err
		}

		r.stop(errJournalStopped)

		return err
	}

	r.seq++
	r.events = append(r.events, *e)
	r.unsent = nil

	return nil
}

func ownError(ctx context.Context, err error) bool {
	if multi, ok := err.(interface{ Unwrap() []error }); ok && len(multi.Unwrap()) > 0 {
		return !slices.ContainsFunc(multi.Unwrap(), func(leaf error) bool { return !ownError(ctx, leaf) })
	}

	if single, ok := err.(interface{ Unwrap() error }); ok && single.Unwrap() != nil {
		return ownError(ctx, single.Unwrap())
	}

	return errors.Is(err, ctx.Err()) || errors.Is(err, context.Cause(ctx))
}

func appendLabel(err error) *KindError {
	if errors.Is(err, ErrJournal) {
		return ErrJournal
	}

	return ErrGraceEnded
}

func (r *runner[S]) stop(reason error) {
	if r.stopReason == nil {
		r.stopReason = reason
	}
}

func (r *runner[S]) causes() []error {
	var errs []error

	if r.rejected != nil {
		errs = append(errs, r.rejected)
	}

	errs = append(errs, r.appendErrs...)

	if r.interrupted != nil {
		errs = append(errs, r.interrupted)
	}

	return errs
}

// Run starts a new execution of the saga on st under ctx and writes the committed state back into st; completion
// returns nil. A state with ExecutionID() string gives the execution ID, else a random one is used, and an ID that
// exists fails with ErrExecutionExists. An error before the saga-started event is stored returns as is, and any later
// end that is not a clean completion returns a *RunError.
func (s *Saga[S]) Run(ctx context.Context, st *S) error {
	execution, err := checkRun(st)
	if err != nil {
		return err
	}

	committed, err := newStore(*st)
	if err != nil {
		return err
	}

	journal := s.journal
	if journal == nil {
		journal = NewMemoryJournal()
	}

	r := &runner[S]{
		saga: s, execution: execution, writer: newID(), journal: journal,
		store: committed, runs: make([]stepRun, len(s.steps)), undone: make([]stepStatus, len(s.steps)),
	}

	grace, release := graceContext(ctx, s.recovery)
	defer release()

	zoned := make([]StepZone, len(s.steps))
	for i := range s.steps {
		zoned[i] = s.steps[i].stepZone()
	}

	if startErr := r.append(grace, &Event{Kind: EventSagaStarted, Steps: zoned}); startErr != nil {
		return startErr
	}

	return r.finish(ctx, grace, r.forward(ctx, grace), st)
}

func (r *runner[S]) finish(ctx, grace context.Context, end forwardEnd, st *S) error {
	if end == endFailed {
		r.backward(grace)
	}

	status := r.status(end)
	sealed := status != "" && r.record(grace, &Event{Kind: EventSagaFinished, Status: status})

	if r.graceHit && r.interrupted == nil {
		r.interrupted = callerCause(ctx)
	}

	causes := r.causes()
	if writeErr := r.store.writeBack(st); writeErr != nil {
		causes = append(causes, writeErr)
	}

	if sealed && status == StatusCompleted && len(causes) == 0 {
		return nil
	}

	var appendKind *KindError
	if len(r.appendErrs) > 0 {
		appendKind = appendLabel(errors.Join(r.appendErrs...))
	}

	kind := endKind(status, r.rejected != nil, appendKind)

	return &RunError{Kind: kind, Steps: r.failures, Pending: r.pending(), causes: causes}
}

func (s runStatus) stepStatus() stepStatus {
	switch s {
	case runSucceeded:
		return stepSucceeded
	case runFailed:
		return stepFailed
	case runSkipped:
		return stepSkipped
	case runNotStarted:
		return stepNotStarted
	case runRunning, runUnknown:
		return stepUnknown
	}

	return stepUnknown
}

func callerCause(ctx context.Context) error {
	cause := context.Cause(ctx)
	if err := ctx.Err(); !errors.Is(cause, err) {
		return errors.Join(cause, err)
	}

	return cause
}

func (r *runner[S]) pending() []string {
	statuses := make([]stepStatus, len(r.saga.steps))
	pivot := stepNotStarted

	for i := range r.saga.steps {
		statuses[i] = r.undone[i]
		if statuses[i] == "" {
			statuses[i] = r.runs[i].status.stepStatus()
		}

		if i == r.saga.pivot {
			pivot = statuses[i]
			if r.runs[i].applied {
				pivot = stepSucceeded
			}
		}
	}

	var pending []string

	for i, status := range statuses {
		if owed(status, r.saga.steps[i].stepZone(), pivot) {
			pending = append(pending, r.saga.steps[i].name)
		}
	}

	return pending
}

func (r *runner[S]) status(end forwardEnd) Status {
	switch {
	case end == endRejected:
		return ""
	case end == endCompleted:
		return StatusCompleted
	case end == endIncomplete:
		return StatusIncomplete
	case slices.ContainsFunc(r.failures, func(e *Error) bool { return e.Compensation }):
		return StatusCompensationFailed
	default:
		return StatusCompensated
	}
}

func endKind(status Status, rejected bool, appendKind *KindError) *KindError {
	switch {
	case rejected:
		return ErrConcurrentWriter
	case status == StatusCompensated:
		return ErrCompensated
	case status == StatusCompensationFailed:
		return ErrCompensationFailed
	case status == StatusCompleted && appendKind != nil:
		return appendKind
	default:
		return ErrIncomplete
	}
}

type waitRelease func()

func graceContext(ctx context.Context, d time.Duration) (context.Context, waitRelease) {
	grace, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	done := make(chan struct{})

	go func() {
		defer close(done)

		select {
		case <-ctx.Done():
		case <-grace.Done():
			return
		}

		timer := time.NewTimer(d)
		defer timer.Stop()

		select {
		case <-timer.C:
			cancel(ErrGraceEnded)
		case <-grace.Done():
		}
	}()

	return grace, func() {
		cancel(nil)
		<-done
	}
}
