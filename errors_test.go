package sagas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	nameFailed  = "failed"
	nameUnknown = "unknown"
	keyStep     = "step"
)

var errLookalike = errors.New("sagas: state conflict")

var (
	errFirst    = errors.New("first attempt")
	errSecond   = errors.New("second attempt")
	errThird    = errors.New("third attempt")
	errReplaced = errors.New("replaced by middleware")
	errStop     = errors.New("caller stopped the run")
)

type kindRow struct {
	name string
	kind *KindError
	want string
}

func kindErrorRows() []kindRow {
	return []kindRow{
		{
			"[Success] When: the kind is ErrInvalidStep ; Should: return its message",
			ErrInvalidStep,
			"sagas: invalid step",
		},
		{
			"[Success] When: the kind is ErrDuplicateStep ; Should: return its message",
			ErrDuplicateStep,
			"sagas: duplicate step",
		},
		{
			"[Success] When: the kind is ErrUnknownDependency ; Should: return its message",
			ErrUnknownDependency, "sagas: unknown dependency",
		},
		{"[Success] When: the kind is ErrCycle ; Should: return its message", ErrCycle, "sagas: cycle"},
		{"[Success] When: the kind is ErrJournal ; Should: return its message", ErrJournal, "sagas: journal failure"},
		{
			"[Success] When: the kind is ErrExecutionExists ; Should: return its message",
			ErrExecutionExists, "sagas: execution exists",
		},
		{
			"[Success] When: the kind is ErrConcurrentWriter ; Should: return its message",
			ErrConcurrentWriter, "sagas: concurrent writer",
		},
		{
			"[Success] When: the kind is ErrInvalidState ; Should: return its message",
			ErrInvalidState,
			"sagas: invalid state",
		},
		{"[Success] When: the kind is ErrConflict ; Should: return its message", ErrConflict, "sagas: state conflict"},
		{
			"[Success] When: the kind is ErrCompensated ; Should: return its message",
			ErrCompensated,
			"sagas: compensated",
		},
		{
			"[Success] When: the kind is ErrCompensationFailed ; Should: return its message",
			ErrCompensationFailed, "sagas: compensation failed",
		},
		{"[Success] When: the kind is ErrIncomplete ; Should: return its message", ErrIncomplete, "sagas: incomplete"},
		{"[Success] When: the kind is ErrInvalidRun ; Should: return its message", ErrInvalidRun, "sagas: invalid run"},
		{
			"[Success] When: the kind is ErrCallNotRun ; Should: return its message",
			ErrCallNotRun, "sagas: middleware returned without running the call",
		},
		{
			"[Success] When: the kind is ErrCallUnfinished ; Should: return its message",
			ErrCallUnfinished, "sagas: middleware returned before the call finished",
		},
		{
			"[Success] When: the kind is ErrCallConcurrent ; Should: return its message",
			ErrCallConcurrent, "sagas: middleware called next while a call was running",
		},
		{
			"[Success] When: the kind is ErrCallContextLost ; Should: return its message",
			ErrCallContextLost, "sagas: middleware passed on a context that lost the call state",
		},
		{
			"[Success] When: the kind is ErrGraceEnded ; Should: return its message",
			ErrGraceEnded, "sagas: recovery grace period ended",
		},
		{"[Success] When: the kind is ErrFailed ; Should: return its message", ErrFailed, "sagas: step failed"},
		{
			"[Success] When: the kind is ErrUnknown ; Should: return its message",
			ErrUnknown,
			"sagas: step outcome unknown",
		},
		{"[Success] When: the kind is ErrPanic ; Should: return its message", ErrPanic, "sagas: panic"},
	}
}

func Test_KindError_Error(t *testing.T) {
	t.Parallel()

	for _, tt := range kindErrorRows() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.kind.Error())
		})
	}
}

func Test_KindError_Name(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind *KindError
		want string
	}{
		{"[Success] When: the kind is ErrInvalidStep ; Should: return its label", ErrInvalidStep, "invalid_step"},
		{"[Success] When: the kind is ErrDuplicateStep ; Should: return its label", ErrDuplicateStep, "duplicate_step"},
		{
			"[Success] When: the kind is ErrUnknownDependency ; Should: return its label",
			ErrUnknownDependency, "unknown_dependency",
		},
		{"[Success] When: the kind is ErrCycle ; Should: return its label", ErrCycle, "cycle"},
		{"[Success] When: the kind is ErrJournal ; Should: return its label", ErrJournal, "journal"},
		{
			"[Success] When: the kind is ErrExecutionExists ; Should: return its label",
			ErrExecutionExists, "execution_exists",
		},
		{
			"[Success] When: the kind is ErrConcurrentWriter ; Should: return its label",
			ErrConcurrentWriter, "concurrent_writer",
		},
		{"[Success] When: the kind is ErrInvalidState ; Should: return its label", ErrInvalidState, "invalid_state"},
		{"[Success] When: the kind is ErrConflict ; Should: return its label", ErrConflict, "conflict"},
		{"[Success] When: the kind is ErrCompensated ; Should: return its label", ErrCompensated, "compensated"},
		{
			"[Success] When: the kind is ErrCompensationFailed ; Should: return its label",
			ErrCompensationFailed, "compensation_failed",
		},
		{"[Success] When: the kind is ErrIncomplete ; Should: return its label", ErrIncomplete, "incomplete"},
		{"[Success] When: the kind is ErrInvalidRun ; Should: return its label", ErrInvalidRun, "invalid_run"},
		{"[Success] When: the kind is ErrCallNotRun ; Should: return its label", ErrCallNotRun, "call_not_run"},
		{
			"[Success] When: the kind is ErrCallUnfinished ; Should: return its label",
			ErrCallUnfinished, "call_unfinished",
		},
		{
			"[Success] When: the kind is ErrCallConcurrent ; Should: return its label",
			ErrCallConcurrent, "call_concurrent",
		},
		{
			"[Success] When: the kind is ErrCallContextLost ; Should: return its label",
			ErrCallContextLost, "call_context_lost",
		},
		{"[Success] When: the kind is ErrGraceEnded ; Should: return its label", ErrGraceEnded, "grace_ended"},
		{"[Success] When: the kind is ErrFailed ; Should: return its label", ErrFailed, nameFailed},
		{"[Success] When: the kind is ErrUnknown ; Should: return its label", ErrUnknown, nameUnknown},
		{"[Success] When: the kind is ErrPanic ; Should: return its label", ErrPanic, "panic"},
		{"[Fail] When: the kind is nil ; Should: return an empty label", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.kind.Name())
		})
	}
}

func Test_Error_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  Error
		want string
	}{
		{
			name: "[Success] When: an action failed ; Should: name the step and its attempts",
			err:  Error{Step: payStep, Attempts: 3, Err: errAlwaysFails},
			want: `sagas: step "pay" failed after 3 attempts: ` + errAlwaysFails.Error(),
		},
		{
			name: "[Success] When: a compensation failed ; Should: name the compensation of the step",
			err:  Error{Step: payStep, Attempts: 1, Compensation: true, Err: errAlwaysFails},
			want: `sagas: compensation of step "pay" failed after 1 attempts: ` + errAlwaysFails.Error(),
		},
		{
			name: "[Success] When: the step never ran ; Should: report 0 attempts",
			err:  Error{Step: "car", Err: ErrJournal},
			want: `sagas: step "car" failed after 0 attempts: ` + ErrJournal.Error(),
		},
		{
			name: "[Success] When: Err is nil ; Should: end after the attempts",
			err:  Error{Step: payStep, Attempts: 2, Cause: errStop},
			want: `sagas: step "pay" failed after 2 attempts; cause: ` + errStop.Error(),
		},
		{
			name: "[Success] When: a context cause is set ; Should: name it after the error",
			err:  Error{Step: payStep, Attempts: 1, Err: context.Canceled, Cause: errStop},
			want: `sagas: step "pay" failed after 1 attempts: context canceled; cause: ` + errStop.Error(),
		},
		{
			name: "[Success] When: earlier errors are set ; Should: list them oldest first",
			err:  Error{Step: payStep, Attempts: 3, Err: errThird, Previous: []error{errFirst, errSecond}},
			want: `sagas: step "pay" failed after 3 attempts: third attempt; previous: first attempt; second attempt`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func Test_Error_Unwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  Error
		want []error
	}{
		{
			name: "[Success] When: every field is set ; Should: return Err, Cause and Previous in order",
			err:  Error{Err: errThird, Cause: errStop, Previous: []error{errFirst, errSecond}},
			want: []error{errThird, errStop, errFirst, errSecond},
		},
		{
			name: "[Success] When: Err is nil ; Should: skip it",
			err:  Error{Cause: errStop, Previous: []error{errFirst}},
			want: []error{errStop, errFirst},
		},
		{
			name: "[Success] When: a previous entry is nil ; Should: skip it",
			err:  Error{Err: errThird, Previous: []error{nil, errFirst}},
			want: []error{errThird, errFirst},
		},
		{
			name: "[Success] When: nothing is set ; Should: return no errors",
			err:  Error{},
			want: []error{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.Unwrap())
		})
	}
}

func Test_Error_Is(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    *Error
		target error
		want   bool
	}{
		{
			name:   "[Success] When: the target is the same Kind ; Should: match",
			err:    &Error{Kind: ErrConflict, Step: payStep},
			target: ErrConflict,
			want:   true,
		},
		{
			name:   "[Success] When: Err wraps the target ; Should: match through the chain",
			err:    &Error{Kind: ErrFailed, Err: fmt.Errorf("pay: %w", errAlwaysFails)},
			target: errAlwaysFails,
			want:   true,
		},
		{
			name:   "[Success] When: Cause is the target ; Should: match through the chain",
			err:    &Error{Kind: ErrUnknown, Err: context.Canceled, Cause: errStop},
			target: errStop,
			want:   true,
		},
		{
			name:   "[Success] When: Previous holds the target ; Should: match through the chain",
			err:    &Error{Kind: ErrFailed, Err: errThird, Previous: []error{errFirst}},
			target: errFirst,
			want:   true,
		},
		{
			name:   "[Fail] When: the target is another Kind ; Should: not match",
			err:    &Error{Kind: ErrFailed},
			target: ErrConflict,
		},
		{
			name:   "[Fail] When: a caller error has the same text as a Kind ; Should: not match",
			err:    &Error{Kind: ErrConflict},
			target: errLookalike,
		},
		{
			name:   "[Fail] When: the target is not a Kind ; Should: not match",
			err:    &Error{Kind: ErrFailed},
			target: errAlwaysFails,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, errors.Is(tt.err, tt.target))
		})
	}
}

func Test_Error_LogValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *Error
		want map[string]any
	}{
		{
			name: "[Success] When: every field is set ; Should: log each one",
			err: &Error{
				Kind: ErrUnknown, Step: payStep, Compensation: true, Attempts: 2,
				Err: errThird, Cause: errStop, Previous: []error{errFirst, errSecond},
			},
			want: map[string]any{
				"kind": nameUnknown, keyStep: payStep, "compensation": true, "attempts": 2.0,
				"err": errThird.Error(), "cause": errStop.Error(),
				"previous": []any{errFirst.Error(), errSecond.Error()},
			},
		},
		{
			name: "[Success] When: only the Kind and the step are set ; Should: log them and leave the rest out",
			err:  &Error{Kind: ErrFailed, Step: payStep},
			want: map[string]any{"kind": nameFailed, keyStep: payStep, "compensation": false, "attempts": 0.0},
		},
		{
			name: "[Success] When: the Kind is nil ; Should: log an empty kind",
			err:  &Error{Step: payStep},
			want: map[string]any{"kind": "", keyStep: payStep, "compensation": false, "attempts": 0.0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			slog.New(slog.NewJSONHandler(&buf, nil)).Info("logged", "failure", tt.err)

			var line map[string]any

			require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
			assert.Equal(t, tt.want, line["failure"])
		})
	}
}

func Test_eventErrorOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *Error
		want EventError
	}{
		{
			name: "[Success] When: every field is set ; Should: store the kind label and the texts",
			err:  &Error{Kind: ErrUnknown, Err: errThird, Cause: errStop, Previous: []error{errFirst, errSecond}},
			want: EventError{
				Kind: nameUnknown, Message: errThird.Error(), Cause: errStop.Error(),
				Previous: []string{errFirst.Error(), errSecond.Error()},
			},
		},
		{
			name: "[Success] When: only Err is set ; Should: leave the cause and the history empty",
			err:  &Error{Kind: ErrConflict, Err: errAlwaysFails},
			want: EventError{Kind: "conflict", Message: errAlwaysFails.Error()},
		},
		{
			name: "[Success] When: the Kind is nil ; Should: store an empty label",
			err:  &Error{Err: errAlwaysFails},
			want: EventError{Message: errAlwaysFails.Error()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, eventErrorOf(tt.err))
		})
	}
}

func Test_kindOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind outcomeKind
		err  error
		want *KindError
	}{
		{
			name: "[Success] When: a caller error failed ; Should: be ErrFailed",
			kind: outcomeFailure, err: errAlwaysFails, want: ErrFailed,
		},
		{
			name: "[Success] When: a caller error left the outcome unknown ; Should: be ErrUnknown",
			kind: outcomeUnknown, err: context.Canceled, want: ErrUnknown,
		},
		{
			name: "[Success] When: the call panicked ; Should: be ErrPanic",
			kind: outcomeUnknown, err: &PanicError{Value: "boom"}, want: ErrPanic,
		},
		{
			name: "[Success] When: the panic value is a package Kind ; Should: still be ErrPanic",
			kind: outcomeUnknown, err: &PanicError{Value: ErrConflict}, want: ErrPanic,
		},
		{
			name: "[Success] When: the commit conflicted ; Should: be ErrConflict",
			kind: outcomeFailure, err: fmt.Errorf("%w: field", ErrConflict), want: ErrConflict,
		},
		{
			name: "[Success] When: the state does not encode ; Should: be ErrInvalidState",
			kind: outcomeFailure, err: Permanent(fmt.Errorf("%w: NaN", ErrInvalidState)), want: ErrInvalidState,
		},
		{
			name: "[Success] When: middleware skipped the call ; Should: be ErrCallNotRun",
			kind: outcomeFailure, err: Permanent(ErrCallNotRun), want: ErrCallNotRun,
		},
		{
			name: "[Success] When: middleware left the call running ; Should: be ErrCallUnfinished",
			kind: outcomeUnknown, err: Permanent(ErrCallUnfinished), want: ErrCallUnfinished,
		},
		{
			name: "[Success] When: the journal failed ; Should: be ErrJournal",
			kind: outcomeFailure, err: fmt.Errorf("%w: down", ErrJournal), want: ErrJournal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Same(t, tt.want, kindOf(tt.kind, tt.err))
		})
	}
}

func failureOf(t *testing.T, err error, step string) *Error {
	t.Helper()

	var joined interface{ Unwrap() []error }

	ok := errors.As(err, &joined)
	require.True(t, ok, "the run error is not a joined error: %v", err)

	for _, e := range joined.Unwrap() {
		var se *Error
		if errors.As(e, &se) && se.Step == step {
			return se
		}
	}

	require.FailNow(t, "no failure for step "+step, "%v", err)

	return nil
}

func resultOf(t *testing.T, got *ran, step string) StepResult {
	t.Helper()

	for i := range got.res.Steps {
		if got.res.Steps[i].Name == step {
			return got.res.Steps[i]
		}
	}

	require.FailNow(t, "no result for step "+step)

	return StepResult{}
}

func errorRows() []runRow {
	return []runRow{
		failedKindRow(),
		panicKindRow(),
		conflictKindRow(),
		siblingCauseRow(),
		goexitHistoryRow(),
		droppedPredicateRow(),
		droppedOutcomeRow(),
		callerCauseRow(),
	}
}

func failedKindRow() runRow {
	return runRow{
		name: "[Fail] When: an action returns a plain error ; Should: report ErrFailed in the error and the event",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a"), fails(After("a"))}
		},
		wantStatus: StatusUnfinished,
		wantErrs:   []error{errAlwaysFails, ErrFailed},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepFailed},
		verify: func(t *testing.T, got *ran) {
			t.Helper()

			se := failureOf(t, got.err, "b")
			assert.Same(t, ErrFailed, se.Kind)
			assert.Equal(t, 3, se.Attempts)
			assert.Len(t, se.Previous, 2)

			event := resultOf(t, got, "b").Err
			assert.Equal(t, nameFailed, event.Kind)
			assert.Len(t, event.Previous, 2)
		},
	}
}

func panicKindRow() runRow {
	return runRow{
		name: "[Fail] When: an action panics with an error value ; Should: report ErrPanic and match the value",
		steps: func(rec *recorder) []Option {
			return []Option{rec.step("a"), Step("b", func(context.Context, *tripState) error {
				panic(errAlwaysFails)
			}, After("a"))}
		},
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{errAlwaysFails, ErrPanic},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown},
		wantPending: []string{"b"},
		verify: func(t *testing.T, got *ran) {
			t.Helper()

			assert.Same(t, ErrPanic, failureOf(t, got.err, "b").Kind)
			assert.Equal(t, "panic", resultOf(t, got, "b").Err.Kind)
		},
	}
}

func conflictKindRow() runRow {
	return runRow{
		name: "[Fail] When: two parallel steps change the same field ; Should: report ErrConflict as the kind",
		steps: func(rec *recorder) []Option {
			return []Option{
				rec.step("a"),
				rec.booking("b", hotelRitz, After("a")),
				napping("c", "plaza", After("a")),
			}
		},
		wantStatus: StatusUnfinished,
		wantErrs:   []error{ErrConflict},
		wantSteps:  map[string]StepStatus{"a": StepSucceeded, "b": StepSucceeded, "c": StepFailed},
		wantState:  tripState{Hotel: hotelRitz},
		verify: func(t *testing.T, got *ran) {
			t.Helper()

			assert.Same(t, ErrConflict, failureOf(t, got.err, "c").Kind)
			assert.Equal(t, "conflict", resultOf(t, got, "c").Err.Kind)
		},
	}
}

func siblingCauseRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails beside a running sibling ; Should: give the sibling the stopper as its cause",
		steps: func(rec *recorder) []Option {
			waits := Step("c", func(ctx context.Context, _ *tripState) error {
				<-ctx.Done()

				return ctx.Err()
			}, After("a"))

			return []Option{rec.step("a"), fails(After("a")), waits}
		},
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{errAlwaysFails, context.Canceled},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepFailed, "c": StepUnknown},
		wantPending: []string{"c"},
		verify: func(t *testing.T, got *ran) {
			t.Helper()

			sibling := failureOf(t, got.err, "c")
			assert.Same(t, ErrUnknown, sibling.Kind)
			require.ErrorIs(t, sibling.Cause, errAlwaysFails)
			assert.Contains(t, sibling.Cause.Error(), quotedB)

			assert.NotEmpty(t, resultOf(t, got, "c").Err.Cause)
		},
	}
}

func goexitHistoryRow() runRow {
	return runRow{
		name: "[Fail] When: an action ends by runtime.Goexit on attempt 2 ; Should: keep the attempts and attempt 1",
		steps: func(rec *recorder) []Option {
			leaves := Step("b", func(ctx context.Context, _ *tripState) error {
				if attemptOf(ctx) == 1 {
					return errFirst
				}

				runtime.Goexit()

				return nil
			}, After("a"))

			return []Option{rec.step("a"), leaves}
		},
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{errGoexit, errFirst},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown},
		wantPending: []string{"b"},
		verify: func(t *testing.T, got *ran) {
			t.Helper()

			se := failureOf(t, got.err, "b")
			assert.Same(t, ErrUnknown, se.Kind)
			assert.Equal(t, 2, se.Attempts)
			require.Len(t, se.Previous, 1)
			require.ErrorIs(t, se.Previous[0], errFirst)
			require.NotErrorIs(t, se.Err, errFirst)

			assert.Equal(t, 2, resultOf(t, got, "b").Attempts)
		},
	}
}

func droppedPredicateRow() runRow {
	return runRow{
		name: "[Fail] When: a predicate fails after a rejection ; Should: report its error and not start the step",
		steps: func(rec *recorder) []Option {
			late := If(func(*tripState) bool {
				time.Sleep(pause)

				panic("late")
			})

			return []Option{rec.step("a"), rec.step("b", After("a")), rec.step("c", After("a"), late)}
		},
		journal:     scripted(map[int]error{5: ErrConcurrentWriter}),
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantText:    []string{"panic: late"},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown, "c": StepNotStarted},
		wantPending: []string{"b"},
		wantTrail:   []string{opened, startOf("a"), doneOf("a"), startOf("b")},
		verify: func(t *testing.T, got *ran) {
			t.Helper()

			assert.Same(t, ErrPanic, failureOf(t, got.err, "c").Kind)
		},
	}
}

func droppedOutcomeRow() runRow {
	return runRow{
		name: "[Fail] When: a sibling fails after a rejection ; Should: report its error and append nothing for it",
		steps: func(rec *recorder) []Option {
			late := Step("c", func(context.Context, *tripState) error {
				time.Sleep(pause)

				return errAlwaysFails
			}, After("a"), Retry(RetryPolicy{Attempts: 1}))

			return []Option{rec.step("a"), rec.step("b", After("a")), late}
		},
		journal:     scripted(map[int]error{6: ErrConcurrentWriter}),
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{ErrConcurrentWriter, errAlwaysFails},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown, "c": StepUnknown},
		wantPending: []string{"b", "c"},
		wantTrail:   []string{opened, startOf("a"), doneOf("a"), startOf("b"), startOf("c")},
		verify: func(t *testing.T, got *ran) {
			t.Helper()

			assert.Same(t, ErrFailed, failureOf(t, got.err, "c").Kind)
		},
	}
}

func callerCauseRow() runRow {
	return runRow{
		name: "[Fail] When: the caller cancels with a cause while a step runs ; Should: match the cause and report it",
		steps: func(rec *recorder) []Option {
			waits := Step("b", func(ctx context.Context, _ *tripState) error {
				<-ctx.Done()

				return ctx.Err()
			}, After("a"), Retry(RetryPolicy{Attempts: 1}))

			return []Option{rec.step("a"), waits}
		},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  StatusUnfinished,
		wantErrs:    []error{errStop, context.Canceled},
		wantSteps:   map[string]StepStatus{"a": StepSucceeded, "b": StepUnknown},
		wantPending: []string{"b"},
		verify: func(t *testing.T, got *ran) {
			t.Helper()

			se := failureOf(t, got.err, "b")
			assert.Same(t, ErrUnknown, se.Kind)
			require.ErrorIs(t, se.Cause, errStop)
		},
	}
}

func Test_callError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ce   *callError
		want string
	}{
		{
			name: "[Success] When: the call error has a cause and a history ; Should: print the error alone",
			ce:   &callError{err: errFirst, cause: errStop, previous: []error{errSecond}},
			want: errFirst.Error(),
		},
		{
			name: "[Success] When: the call error has only an error ; Should: print it",
			ce:   &callError{err: errSecond},
			want: errSecond.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.ce.Error())
		})
	}
}

func Test_callError_Unwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ce     *callError
		wantIs error
		notIs  error
	}{
		{
			name:   "[Success] When: the call error has a cause and a history ; Should: unwrap to the error alone",
			ce:     &callError{err: errFirst, cause: errStop, previous: []error{errSecond}},
			wantIs: errFirst,
			notIs:  errSecond,
		},
		{
			name:   "[Success] When: the error is permanent ; Should: keep the mark visible to the retry loop",
			ce:     &callError{err: Permanent(errFirst), previous: []error{errSecond}},
			wantIs: errPermanent,
			notIs:  errStop,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tt.ce, tt.wantIs)
			require.NotErrorIs(t, tt.ce, tt.notIs)
			require.NotErrorIs(t, tt.ce, tt.ce.cause)
		})
	}
}

func Test_withCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		wantErr   error
		wantCause error
	}{
		{
			name:      "[Success] When: the error is plain ; Should: hold the cause and keep the error",
			err:       errFirst,
			wantErr:   errFirst,
			wantCause: errStop,
		},
		{
			name:      "[Success] When: a cause is already set ; Should: keep the first cause",
			err:       withCause(errFirst, errSecond),
			wantErr:   errFirst,
			wantCause: errSecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ce := asCallError(withCause(tt.err, errStop))

			assert.Same(t, tt.wantErr, ce.err)
			assert.Same(t, tt.wantCause, ce.cause)
		})
	}
}

func Test_withPrevious(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		err   error
		prior []error
		want  []error
	}{
		{
			name: "[Success] When: there are no earlier errors ; Should: return the error unchanged",
			err:  errThird,
		},
		{
			name:  "[Success] When: the error is plain ; Should: hold the earlier errors",
			err:   errThird,
			prior: []error{errFirst, errSecond},
			want:  []error{errFirst, errSecond},
		},
		{
			name:  "[Success] When: middleware replaced the call error ; Should: put the earlier errors first",
			err:   replaced(errThird, errSecond),
			prior: []error{errFirst},
			want:  []error{errFirst, errSecond},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := withPrevious(tt.err, tt.prior)

			require.ErrorIs(t, got, errThird)
			assert.Equal(t, tt.want, asCallError(got).previous)
		})
	}
}

func Test_replaced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          error
		original     error
		wantPrevious []error
	}{
		{
			name:     "[Success] When: there is no original error ; Should: return the error unchanged",
			err:      errReplaced,
			original: nil,
		},
		{
			name:     "[Success] When: the error wraps the original ; Should: return the error unchanged",
			err:      fmt.Errorf("wrapped: %w", errFirst),
			original: errFirst,
		},
		{
			name:         "[Success] When: the error replaces the original ; Should: keep the original as history",
			err:          errReplaced,
			original:     errFirst,
			wantPrevious: []error{errFirst},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := replaced(tt.err, tt.original)

			require.ErrorIs(t, got, tt.err)
			assert.Equal(t, tt.wantPrevious, asCallError(got).previous)
		})
	}
}

func Test_record(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ce   *callError
		want [][]error
	}{
		{
			name: "[Success] When: the attempt has only an error ; Should: record it",
			ce:   &callError{err: errFirst},
			want: [][]error{{errFirst}},
		},
		{
			name: "[Success] When: the attempt replaced an earlier error ; Should: record it first",
			ce:   &callError{err: errSecond, previous: []error{errFirst}},
			want: [][]error{{errFirst}, {errSecond}},
		},
		{
			name: "[Success] When: the attempt has a cause ; Should: record the error with the cause joined",
			ce:   &callError{err: errFirst, cause: errStop},
			want: [][]error{{errFirst, errStop}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := record(tt.ce)

			require.Len(t, got, len(tt.want))

			for i, wants := range tt.want {
				for _, want := range wants {
					require.ErrorIs(t, got[i], want)
				}
			}
		})
	}
}
