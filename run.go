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

// ErrInvalidRun is returned by Run for a run option or an execution ID that breaks a rule of the run input.
var ErrInvalidRun = errors.New("sagas: invalid run")

const (
	keyWithExecutionID = "WithExecutionID"
	idBytes            = 16
)

// RunOption configures one run. Pass it to Run.
type RunOption func(*runConfig)

type runConfig struct {
	execution string
	given     map[string]int
}

// WithExecutionID sets the execution ID of the run. Left out, the run uses a random 128-bit hex ID.
func WithExecutionID(id string) RunOption {
	return func(c *runConfig) {
		c.execution = id
		c.given[keyWithExecutionID]++
	}
}

// StepError reports the step whose action or compensation failed, after how many attempts, and why.
type StepError struct {
	Step         string
	Attempts     int
	Compensation bool
	Err          error
}

// Error names the step, whether its compensation failed, the attempts and the cause.
func (e *StepError) Error() string {
	what := "step"
	if e.Compensation {
		what = "compensation of step"
	}

	return fmt.Sprintf("sagas: %s %q failed after %d attempts: %v", what, e.Step, e.Attempts, e.Err)
}

// Unwrap returns the cause of the failure.
func (e *StepError) Unwrap() error {
	return e.Err
}

func checkRun[S any](s *S, opts []RunOption) (runConfig, error) {
	cfg := runConfig{given: map[string]int{}}
	nilOptions := 0

	for _, opt := range opts {
		if opt == nil {
			nilOptions++

			continue
		}

		opt(&cfg)
	}

	if cfg.given[keyWithExecutionID] == 0 {
		cfg.execution = newID()
	}

	rules := append(checkRepeats(cfg.given, nilOptions, "RunOption"), checkName(cfg.execution)...)

	errs := make([]error, 0, len(rules)+1)
	for _, rule := range rules {
		errs = append(errs, fmt.Errorf("%w: %s", ErrInvalidRun, rule))
	}

	if s == nil {
		errs = append(errs, fmt.Errorf("%w: state is nil", ErrInvalidState))
	} else if _, err := encodeFields(reflect.ValueOf(*s)); err != nil {
		errs = append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		return runConfig{}, err
	}

	return cfg, nil
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
	store       *store[S]
	runs        []stepRun
	failures    []*StepError
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

			return err
		}

		err = fmt.Errorf("%w: %w", ErrJournal, err)
		r.journalErrs = append(r.journalErrs, err)

		return err
	}

	r.seq++
	r.events = append(r.events, stored)

	return nil
}

func (r *runner[S]) errs() []error {
	var errs []error

	for _, failure := range r.failures {
		errs = append(errs, failure)
	}

	if r.rejected != nil {
		errs = append(errs, r.rejected)
	}

	return append(errs, r.journalErrs...)
}

// Run executes the saga on the state st under ctx and returns the Result of the execution. A completed run returns a
// nil error and writes the committed state back into st.
func (s *Saga[S]) Run(ctx context.Context, st *S, opts ...RunOption) (Result, error) {
	cfg, err := checkRun(st, opts)
	if err != nil {
		return Result{}, err
	}

	committed, err := newStore(*st)
	if err != nil {
		return Result{}, err
	}

	journal := s.journal
	if journal == nil {
		journal = newMemoryJournal()
	}

	r := &runner[S]{
		saga: s, execution: cfg.execution, writer: newID(), journal: journal,
		store: committed, runs: make([]stepRun, len(s.steps)),
	}

	grace, release := graceContext(ctx, s.recovery)
	defer release()

	zoned := make([]StepZone, len(s.steps))
	for i := range s.steps {
		zoned[i] = StepZone{Name: s.steps[i].name, Zone: s.steps[i].zone}
	}

	if startErr := r.append(grace, &Event{Kind: EventSagaStarted, Steps: zoned}); startErr != nil {
		return Result{}, startErr
	}

	end := r.forward(ctx, grace)
	sealed := end == endCompleted && r.record(grace, &Event{Kind: EventSagaFinished, Status: StatusCompleted})

	errs := r.errs()
	if err = committed.writeBack(st); err != nil {
		errs = append(errs, err)
	}

	res := buildResult(r.events)
	if end == endCompleted && !sealed && r.rejected == nil {
		res.Status = StatusCompleted
	}

	return res, errors.Join(errs...)
}

type waitRelease func()

func graceContext(ctx context.Context, d time.Duration) (context.Context, waitRelease) {
	grace, cancel := context.WithCancel(context.WithoutCancel(ctx))
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
			cancel()
		case <-grace.Done():
		}
	}()

	return grace, func() {
		cancel()
		<-done
	}
}
