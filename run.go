package sagas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
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
	journalErrs []error
	rejected    error
	stopReason  error
	store       *store[S]
	runs        []stepRun
	failures    []*Error
}

func (r *runner[S]) append(ctx context.Context, e *Event) error {
	stored := *e
	stored.Saga = r.saga.name
	stored.Execution = r.execution
	stored.Writer = r.writer
	stored.Seq = r.seq + 1
	stored.Time = time.Now()
	cloneSlices(&stored)

	if err := r.journal.Append(ctx, &stored); err != nil {
		if errors.Is(err, ErrExecutionExists) || errors.Is(err, ErrConcurrentWriter) {
			r.rejected = err
			r.stop(errRejectionStopped)

			return err
		}

		err = fmt.Errorf("%w: %w", ErrJournal, err)
		r.journalErrs = append(r.journalErrs, err)
		r.stop(errJournalStopped)

		return err
	}

	r.seq++
	r.events = append(r.events, stored)

	return nil
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

	return append(errs, r.journalErrs...)
}

// Run executes the saga on the state st under ctx. A completed run returns nil and writes the committed state back
// into st. A state that declares ExecutionID() string gives the execution ID, and any other state gets a random one.
// An error before the saga-started event is stored is returned as is. Any later end that is not a clean completion
// returns a *RunError.
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
		store: committed, runs: make([]stepRun, len(s.steps)),
	}

	grace, release := graceContext(ctx, s.recovery)
	defer release()

	zoned := make([]StepZone, len(s.steps))
	for i := range s.steps {
		zoned[i] = StepZone{Name: s.steps[i].name, Zone: s.steps[i].zone}
	}

	if startErr := r.append(grace, &Event{Kind: EventSagaStarted, Steps: zoned}); startErr != nil {
		return startErr
	}

	end := r.forward(ctx, grace)
	sealed := end == endCompleted && r.record(grace, &Event{Kind: EventSagaFinished, Status: StatusCompleted})

	causes := r.causes()
	if writeErr := committed.writeBack(st); writeErr != nil {
		causes = append(causes, writeErr)
	}

	if sealed && len(causes) == 0 {
		return nil
	}

	res := buildResult(r.events)

	return &RunError{Kind: ErrIncomplete, Steps: r.failures, Pending: res.pending(), causes: causes}
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
