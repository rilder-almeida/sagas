package sagastest

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"testing"

	"github.com/rilder-almeida/sagas"
)

const (
	caseNeverApplied = "never applied"
	caseTwice        = "compensation runs twice"
	caseLate         = "step lands after its compensation"
	caseUnknown      = "unknown outcome"
	caseRepeated     = "step repeated"
)

var errTrigger = errors.New("sagastest: injected failure of the step action")

func cleanRun[S any, W Services[S]](ctx context.Context, name string, state S, probe W) (plan, *S, error) {
	p, err := planOf[S](probe.Flow())
	if err != nil {
		return plan{}, nil, fmt.Errorf("%w: Flow is not a valid flow: %w", ErrCleanRun, err)
	}

	rec := NewRecorder()

	final, err := runCase(ctx, name, state, probe, rec)
	if err != nil {
		return plan{}, nil, fmt.Errorf("%w: the run failed: %w", ErrCleanRun, err)
	}

	attempts := rec.Attempts()
	if len(attempts) == 0 {
		return plan{}, nil, fmt.Errorf("%w: the run recorded no call, so every step is disabled in the given state",
			ErrCleanRun)
	}

	var errs []error

	for _, attempt := range attempts {
		if !slices.ContainsFunc(p.steps, func(s sagas.StepReport) bool { return s.Name == attempt.Step }) {
			errs = append(errs, fmt.Errorf("%w: step %q ran, but Flow does not name it", ErrCleanRun, attempt.Step))
		}
	}

	if unnamed := errors.Join(errs...); unnamed != nil {
		return plan{}, nil, unnamed
	}

	return p, final, nil
}

// CompensationCases runs the clean run of probe, then returns three cases for each step with a compensation: never
// applied, compensation runs twice, and step lands after its compensation. Each case must end in sagas.ErrCompensated.
func CompensationCases[S any, W CompensationServices[S]](
	ctx context.Context, name string, state S, probe W,
) (iter.Seq[Case[W]], error) {
	p, _, err := cleanRun(ctx, name, state, probe)
	if err != nil {
		return nil, err
	}

	var steps []string

	for i := range p.steps {
		if p.steps[i].Compensate {
			steps = append(steps, p.steps[i].Name)
		}
	}

	if len(steps) == 0 {
		return nil, fmt.Errorf("%w: the flow has no step with a compensation", ErrNoStepToTest)
	}

	return func(yield func(Case[W]) bool) {
		for _, step := range steps {
			for _, kind := range []string{caseNeverApplied, caseTwice, caseLate} {
				if !yield(compensationCase[S, W](name, state, step, kind)) {
					return
				}
			}
		}
	}, nil
}

func compensationCase[S any, W CompensationServices[S]](name string, state S, step, kind string) Case[W] {
	faults := []sagas.Middleware{Fail(step, sagas.Permanent(errTrigger), OnPhase(sagas.PhaseExecute))}
	if kind == caseTwice {
		faults = []sagas.Middleware{Panic(step, OnPhase(sagas.PhaseExecute)), repeated(step, sagas.PhaseCompensate)}
	}

	return Case[W]{Name: step + " " + kind, run: func(ctx context.Context, services W) error {
		rec := NewRecorder()
		final, err := runCase(ctx, name, state, services, rec, faults...)

		attempts := rec.Attempts()

		i := slices.IndexFunc(attempts, func(a Attempt) bool { return a.Step == step && a.Phase == sagas.PhaseExecute })
		if i < 0 {
			return caseFailure(ErrNotStarted, step, kind, "its compensation is untested", err)
		}

		if !errors.Is(err, sagas.ErrCompensated) {
			return caseFailure(ErrOutcome, step, kind, "Run did not return sagas.ErrCompensated", err)
		}

		var lateErr error
		if kind == caseLate {
			lateErr = services.ExecuteLate(ctx, final, step, attempts[i].IdempotencyKey())
		}

		if checkErr := services.Check(ctx, final); checkErr != nil {
			return caseFailure(ErrCheck, step, kind, "Check", errors.Join(checkErr, lateErr))
		}

		return nil
	}}
}

func repeated(step string, phase sagas.Phase) sagas.MiddlewareFunc {
	return func(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
		if info.Step != step || info.Phase != phase {
			return call(ctx)
		}

		if err := call(ctx); err != nil {
			return err
		}

		return call(ctx)
	}
}

// PivotCases runs the clean run of probe, then returns one case for the unknown outcome of the pivot and one repeat
// case for each forward-only step. With one attempt, the pivot case must end in sagas.ErrIncomplete.
func PivotCases[S any, W Services[S]](ctx context.Context, name string, state S, probe W) (iter.Seq[Case[W]], error) {
	p, clean, err := cleanRun(ctx, name, state, probe)
	if err != nil {
		return nil, err
	}

	if p.pivot == "" {
		return nil, fmt.Errorf("%w: the flow has no pivot", ErrNoStepToTest)
	}

	return func(yield func(Case[W]) bool) {
		if !yield(pivotCase[S, W](name, state, p, clean)) {
			return
		}

		for _, step := range p.forwardOnly {
			if !yield(repeatCase[S, W](name, state, step)) {
				return
			}
		}
	}, nil
}

func pivotAttempts(p plan) int {
	for i := range p.steps {
		if p.steps[i].Name == p.pivot && p.steps[i].RetryPolicy.Attempts > 0 {
			return p.steps[i].RetryPolicy.Attempts
		}
	}

	return sagas.DefaultAttempts
}

func started(rec *Recorder, step string, phase sagas.Phase) bool {
	return slices.ContainsFunc(rec.Attempts(), func(a Attempt) bool {
		return a.Step == step && a.Phase == phase
	})
}

func pivotCase[S any, W Services[S]](name string, state S, p plan, clean *S) Case[W] {
	return Case[W]{Name: p.pivot + " " + caseUnknown, run: func(ctx context.Context, services W) error {
		rec := NewRecorder()
		fault := Unknown(p.pivot, OnPhase(sagas.PhasePivot), OnAttempts(1))
		final, err := runCase(ctx, name, state, services, rec, fault)

		if !started(rec, p.pivot, sagas.PhasePivot) {
			return caseFailure(ErrNotStarted, p.pivot, caseUnknown, "the pivot is untested", err)
		}

		var outcome error
		if pivotAttempts(p) > 1 {
			outcome = completedPivot(p.pivot, err, final, clean)
		} else {
			outcome = incompletePivot(p.pivot, err, rec)
		}

		return errors.Join(outcome, checked(ctx, services, final, p.pivot, caseUnknown))
	}}
}

func checked[S any, W Services[S]](ctx context.Context, services W, final *S, step, kind string) error {
	if err := services.Check(ctx, final); err != nil {
		return caseFailure(ErrCheck, step, kind, "Check", err)
	}

	return nil
}

func completedPivot[S any](pivot string, err error, got, clean *S) error {
	if err != nil {
		return caseFailure(ErrOutcome, pivot, caseUnknown, "Run did not return nil", err)
	}

	if !reflect.DeepEqual(got, clean) {
		return caseFailure(ErrOutcome, pivot, caseUnknown,
			fmt.Sprintf("the final state %+v differs from the clean run %+v", *got, *clean), nil)
	}

	return nil
}

func incompletePivot(pivot string, err error, rec *Recorder) error {
	if !errors.Is(err, sagas.ErrIncomplete) {
		return caseFailure(ErrOutcome, pivot, caseUnknown, "Run did not return sagas.ErrIncomplete", err)
	}

	var errs []error

	runErr, ok := errors.AsType[*sagas.RunError](err)
	if !ok || !slices.Equal(runErr.Pending, []string{pivot}) {
		var pending []string
		if ok {
			pending = runErr.Pending
		}

		errs = append(errs, caseFailure(ErrOutcome, pivot, caseUnknown,
			fmt.Sprintf("pending steps are %q, want only the pivot", pending), nil))
	}

	if slices.ContainsFunc(rec.Attempts(), func(a Attempt) bool { return a.Phase == sagas.PhaseCompensate }) {
		errs = append(errs, caseFailure(ErrOutcome, pivot, caseUnknown, "a compensation ran", nil))
	}

	return errors.Join(errs...)
}

func repeatCase[S any, W Services[S]](name string, state S, step string) Case[W] {
	return Case[W]{Name: step + " " + caseRepeated, run: func(ctx context.Context, services W) error {
		rec := NewRecorder()
		final, err := runCase(ctx, name, state, services, rec, repeated(step, sagas.PhaseExecute))

		if !started(rec, step, sagas.PhaseExecute) {
			return caseFailure(ErrNotStarted, step, caseRepeated, "its repeat is untested", err)
		}

		var outcome error
		if err != nil {
			outcome = caseFailure(ErrOutcome, step, caseRepeated, "Run did not return nil", err)
		}

		return errors.Join(outcome, checked(ctx, services, final, step, caseRepeated))
	}}
}

// TestCompensations runs each case of CompensationCases in a subtest named after it. It calls newServices with t for
// the clean run and with the t of the subtest for each case. Each call must return a fresh value over fresh fakes.
func TestCompensations[S any, W CompensationServices[S]](
	t *testing.T, name string, state S, newServices func(*testing.T) W,
) {
	t.Helper()

	cases, err := CompensationCases(t.Context(), name, state, newServices(t))
	if err != nil {
		t.Fatal(err)
	}

	runCases(t, cases, newServices)
}

// TestPivot runs each case of PivotCases in a subtest named after it, as TestCompensations does.
func TestPivot[S any, W Services[S]](t *testing.T, name string, state S, newServices func(*testing.T) W) {
	t.Helper()

	cases, err := PivotCases(t.Context(), name, state, newServices(t))
	if err != nil {
		t.Fatal(err)
	}

	runCases(t, cases, newServices)
}

func runCases[W any](t *testing.T, cases iter.Seq[Case[W]], newServices func(*testing.T) W) {
	t.Helper()

	for c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if err := c.Run(t.Context(), newServices(t)); err != nil {
				t.Error(err)
			}
		})
	}
}
