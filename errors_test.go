package sagas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	nameFailed   = "failed"
	nameUnknown  = "unknown"
	keyStep      = "step"
	namePanic    = "panic"
	nameConflict = "conflict"
)

var (
	errLookalike            = errors.New("sagas: invalid state")
	errLookalikeCompensated = errors.New("sagas: compensated")
)

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
		{"[Success] When: the kind is ErrNoEvents ; Should: return its message", ErrNoEvents, "sagas: no events"},
		{"[Success] When: the kind is ErrNoSecret ; Should: return its message", ErrNoSecret, "sagas: no secret"},
		{
			"[Success] When: the kind is ErrInvalidState ; Should: return its message",
			ErrInvalidState,
			"sagas: invalid state",
		},
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
			ErrCallConcurrent, "sagas: middleware ran the call again while it was running",
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
		{"[Success] When: the kind is ErrInvalidStep ; Should: return its label", ErrInvalidStep, "invalid-step"},
		{"[Success] When: the kind is ErrDuplicateStep ; Should: return its label", ErrDuplicateStep, "duplicate-step"},
		{"[Success] When: the kind is ErrCycle ; Should: return its label", ErrCycle, "cycle"},
		{"[Success] When: the kind is ErrJournal ; Should: return its label", ErrJournal, "journal"},
		{
			"[Success] When: the kind is ErrExecutionExists ; Should: return its label",
			ErrExecutionExists, "execution-exists",
		},
		{
			"[Success] When: the kind is ErrConcurrentWriter ; Should: return its label",
			ErrConcurrentWriter, "concurrent-writer",
		},
		{"[Success] When: the kind is ErrNoEvents ; Should: return its label", ErrNoEvents, "no-events"},
		{"[Success] When: the kind is ErrNoSecret ; Should: return its label", ErrNoSecret, "no-secret"},
		{"[Success] When: the kind is ErrInvalidState ; Should: return its label", ErrInvalidState, "invalid-state"},
		{"[Success] When: the kind is ErrCompensated ; Should: return its label", ErrCompensated, "compensated"},
		{
			"[Success] When: the kind is ErrCompensationFailed ; Should: return its label",
			ErrCompensationFailed, "compensation-failed",
		},
		{"[Success] When: the kind is ErrIncomplete ; Should: return its label", ErrIncomplete, "incomplete"},
		{"[Success] When: the kind is ErrInvalidRun ; Should: return its label", ErrInvalidRun, "invalid-run"},
		{"[Success] When: the kind is ErrCallNotRun ; Should: return its label", ErrCallNotRun, "call-not-run"},
		{
			"[Success] When: the kind is ErrCallUnfinished ; Should: return its label",
			ErrCallUnfinished, "call-unfinished",
		},
		{
			"[Success] When: the kind is ErrCallConcurrent ; Should: return its label",
			ErrCallConcurrent, "call-concurrent",
		},
		{"[Success] When: the kind is ErrGraceEnded ; Should: return its label", ErrGraceEnded, "grace-ended"},
		{"[Success] When: the kind is ErrFailed ; Should: return its label", ErrFailed, nameFailed},
		{"[Success] When: the kind is ErrUnknown ; Should: return its label", ErrUnknown, nameUnknown},
		{"[Success] When: the kind is ErrPanic ; Should: return its label", ErrPanic, "panic"},
		{
			"[Success] When: the kind is ErrInvalidEvent ; Should: return its label",
			ErrInvalidEvent, "invalid-event",
		},
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
			want: `sagas: step "pay" did not succeed after 3 attempts: ` + errAlwaysFails.Error(),
		},
		{
			name: "[Success] When: a compensation failed ; Should: name the compensation of the step",
			err:  Error{Step: payStep, Attempts: 1, Compensation: true, Err: errAlwaysFails},
			want: `sagas: compensation of step "pay" did not succeed after 1 attempts: ` + errAlwaysFails.Error(),
		},
		{
			name: "[Success] When: the step never ran ; Should: say it did not run",
			err:  Error{Step: "car", Err: ErrJournal},
			want: `sagas: step "car" did not run: ` + ErrJournal.Error(),
		},
		{
			name: "[Success] When: a compensation never ran ; " +
				"Should: say it did not run and keep the cause and history",
			err: Error{
				Step: "car", Compensation: true, Err: ErrGraceEnded, Cause: errStop, Previous: []error{errFirst},
			},
			want: `sagas: compensation of step "car" did not run: ` + ErrGraceEnded.Error() + "; cause: " +
				errStop.Error() + "; previous: first attempt",
		},
		{
			name: "[Success] When: Err is nil ; Should: end after the attempts",
			err:  Error{Step: payStep, Attempts: 2, Cause: errStop},
			want: `sagas: step "pay" did not succeed after 2 attempts; cause: ` + errStop.Error(),
		},
		{
			name: "[Success] When: a context cause is set ; Should: name it after the error",
			err:  Error{Step: payStep, Attempts: 1, Err: context.Canceled, Cause: errStop},
			want: `sagas: step "pay" did not succeed after 1 attempts: context canceled; cause: ` + errStop.Error(),
		},
		{
			name: "[Success] When: earlier errors are set ; Should: list them oldest first",
			err:  Error{Step: payStep, Attempts: 3, Err: errThird, Previous: []error{errFirst, errSecond}},
			want: `sagas: step "pay" did not succeed after 3 attempts: third attempt; ` +
				"previous: first attempt; second attempt",
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
			err:    &Error{Kind: ErrInvalidState, Step: payStep},
			target: ErrInvalidState,
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
			target: ErrInvalidState,
		},
		{
			name:   "[Fail] When: a caller error has the same text as a Kind ; Should: not match",
			err:    &Error{Kind: ErrInvalidState},
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
			err:  &Error{Kind: ErrInvalidState, Err: errAlwaysFails},
			want: EventError{Kind: "invalid-state", Message: errAlwaysFails.Error()},
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
		name  string
		kind  outcomeKind
		label *KindError
		want  *KindError
	}{
		{
			name: "[Success] When: a caller error failed and no label is set ; Should: be ErrFailed",
			kind: outcomeFailure, want: ErrFailed,
		},
		{
			name: "[Success] When: the outcome is unknown and no label is set ; Should: be ErrUnknown",
			kind: outcomeUnknown, want: ErrUnknown,
		},
		{
			name: "[Success] When: the package labelled a panic ; Should: be ErrPanic",
			kind: outcomeUnknown, label: ErrPanic, want: ErrPanic,
		},
		{
			name: "[Success] When: the package labelled a refused commit ; Should: be ErrInvalidState",
			kind: outcomeFailure, label: ErrInvalidState, want: ErrInvalidState,
		},
		{
			name: "[Success] When: the package labelled middleware that skipped the call ; Should: be ErrCallNotRun",
			kind: outcomeFailure, label: ErrCallNotRun, want: ErrCallNotRun,
		},
		{
			name: "[Success] When: the package labelled a call left running ; Should: be ErrCallUnfinished",
			kind: outcomeUnknown, label: ErrCallUnfinished, want: ErrCallUnfinished,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Same(t, tt.want, kindOf(tt.kind, tt.label))
		})
	}
}

func assertFailures(t *testing.T, tt *runRow, got *ran, res *runRecord) {
	t.Helper()

	failures := failuresOf(got.err)
	assert.ElementsMatch(t, slices.Collect(maps.Keys(tt.wantKinds)), slices.Collect(maps.Keys(failures)),
		"failures in %v", got.err)
	assert.ElementsMatch(t, slices.Collect(maps.Keys(tt.wantKinds)), slices.Collect(maps.Keys(tt.wantAttempts)),
		"every failed step needs its attempts")
	assert.ElementsMatch(t, slices.Collect(maps.Keys(tt.wantKinds)), slices.Collect(maps.Keys(tt.wantErrText)),
		"every failed step needs its error text")

	for step, n := range failures {
		assert.Equal(t, 1, n, "failures of step %s", step)
	}

	for step, kind := range tt.wantKinds {
		se := failureOf(t, got.err, step)
		assert.Same(t, kind, se.Kind)
		require.ErrorIs(t, se, kind)
		assert.Equal(t, tt.wantAttempts[step], se.Attempts)
		require.NotEmpty(t, tt.wantErrText[step], "expected error text of step %s", step)
		assert.Contains(t, se.Err.Error(), tt.wantErrText[step])

		assertPrevious(t, se.Previous, tt.wantPreviousIs[step])
		assertCause(t, se.Cause, causeList(tt.wantCauseIs[step]), tt.wantCauseText[step])

		recorded := outcomeOf(res.events, step)
		if slices.Contains(tt.wantUnrecorded, step) {
			assert.Equal(t, EventError{}, recorded.Err)
			assert.Zero(t, recorded.Attempts)

			continue
		}

		assert.Equal(t, se.Attempts, recorded.Attempts)
		assertEventMirror(t, recorded.Err, se)
	}

	for step, kind := range tt.wantNotKinds {
		require.NotErrorIs(t, failureOf(t, got.err, step), kind)
	}

	for step, cause := range tt.wantNotCauseIs {
		require.NotErrorIs(t, failureOf(t, got.err, step).Cause, cause)
	}

	for step, want := range tt.wantCauseKind {
		kind, found := errors.AsType[*KindError](failureOf(t, got.err, step).Cause)
		require.True(t, found, "no *KindError in the cause of step %s", step)
		assert.Same(t, want, kind)
	}

	for step, want := range tt.wantClosed {
		closing := outcomeOf(res.events, step)
		assert.Equal(t, EventStepFailed, closing.Kind, "closing event of step %s", step)
		assert.Zero(t, closing.Attempts, "closing event of step %s", step)
		assert.Equal(t, want, closing.Err, "closing event of step %s", step)
	}

	for step, text := range tt.wantEventCauseText {
		assert.Contains(t, outcomeOf(res.events, step).Err.Cause, text)
	}

	for text, count := range tt.wantTextCount {
		assert.Equal(t, count, strings.Count(got.err.Error(), text))
	}
}

func failuresOf(err error) map[string]int {
	failures := map[string]int{}

	if runErr, ok := errors.AsType[*RunError](err); ok {
		for _, se := range runErr.Steps {
			failures[keyOf(se)]++
		}
	}

	return failures
}

func causeList(cause error) []error {
	if cause == nil {
		return nil
	}

	return []error{cause}
}

func assertEventMirror(t *testing.T, event EventError, se *Error) {
	t.Helper()

	assert.Equal(t, se.Kind.Name(), event.Kind)
	assert.Equal(t, se.Err.Error(), event.Message)

	if se.Cause == nil {
		assert.Empty(t, event.Cause)
	} else {
		assert.Equal(t, se.Cause.Error(), event.Cause)
	}

	require.Len(t, event.Previous, len(se.Previous))

	for i := range se.Previous {
		assert.Equal(t, se.Previous[i].Error(), event.Previous[i])
	}
}

func failureOf(t *testing.T, err error, step string) *Error {
	t.Helper()

	runErr, ok := errors.AsType[*RunError](err)
	require.True(t, ok, "the run error is not a RunError: %v", err)

	for _, se := range runErr.Steps {
		if keyOf(se) == step {
			return se
		}
	}

	require.FailNow(t, "no failure for step "+step, "%v", err)

	return nil
}

func outcomeOf(events []Event, key string) Event {
	step, undo := strings.CutPrefix(key, compPrefix)

	kinds := []EventKind{EventStepSucceeded, EventStepFailed, EventStepUnknown}
	if undo {
		kinds = []EventKind{EventCompensationFailed}
	}

	var last Event

	for i := range events {
		if events[i].Step == step && slices.Contains(kinds, events[i].Kind) {
			last = events[i]
		}
	}

	return last
}

const compPrefix = "comp:"

func comp(step string) string { return compPrefix + step }

func keyOf(se *Error) string {
	if se.Compensation {
		return comp(se.Step)
	}

	return se.Step
}

func errorRows() []runRow {
	return []runRow{
		failedKindRow(),
		panicKindRow(),
		siblingCauseRow(),
		goexitHistoryRow(),
		droppedPredicateRow(),
		droppedOutcomeRow(),
		callerCauseRow(),
		callerInvalidStateRow(),
		callerJournalRow(),
		callerPanicRow(),
		rejectionStopRow(),
		journalStopRow(),
		markedPanicRow(),
		nestedPermanentRow(),
		nestedMiddlewareRow(),
	}
}

func waitingSibling() func(*recorder) Flow {
	return func(rec *recorder) Flow {
		waits := waiting("c")
		waits.RetryPolicy = NoRetry()

		return Sequence(rec.step("a"), Parallel(rec.step("b"), waits))
	}
}

func rejectionStopRow() runRow {
	return runRow{
		name:         "[Fail] When: the journal rejects an event beside a sibling ; Should: name it as the cause",
		steps:        waitingSibling(),
		script:       map[int]error{6: ErrConcurrentWriter},
		wantStatus:   noEndStatus,
		wantKind:     ErrConcurrentWriter,
		wantAttempts: map[string]int{"c": 1},
		wantTrail:    afterA(together(startOf("b"), startOf("c"))),
		wantErrs:     []error{ErrConcurrentWriter, context.Canceled},
		wantNotErrs:  []error{ErrJournal},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown, "c": stepUnknown},
		wantKinds:    map[string]*KindError{"c": ErrUnknown},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": context.Canceled.Error()},

		wantUnrecorded: []string{"c"},
		wantCauseText:  map[string]string{"c": "journal rejected an event"},
	}
}

func journalStopRow() runRow {
	return runRow{
		name: "[Fail] When: the journal fails an append beside a running sibling ; " +
			"Should: name it as the cause",
		steps:        waitingSibling(),
		script:       map[int]error{6: errJournalDown},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantAttempts: map[string]int{"c": 1},
		wantTrail:    afterA(together(startOf("b"), startOf("c"), doneOf("b"), lostOf("c")), seq(closed)),
		wantErrs:     []error{ErrJournal, errJournalDown, context.Canceled},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded, "c": stepUnknown},
		wantKinds:    map[string]*KindError{"c": ErrUnknown},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": context.Canceled.Error()},

		wantCauseText: map[string]string{"c": "journal failure stopped"},
	}
}

var errMarker = errors.New("marked by middleware")

func marking() MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		if err := call(ctx); err != nil {
			return fmt.Errorf("%w: %w", errMarker, err)
		}

		return nil
	}
}

func markedPanicRow() runRow {
	return runRow{
		name: "[Fail] When: middleware wraps a panicking action ; Should: keep the marker and report ErrPanic",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), act("b", func(context.Context, tripState) error { panic(errAlwaysFails) }))
		},
		opts:         []Option{WithMiddleware(marking())},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantAttempts: map[string]int{"b": 1},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b"), closed),
		wantErrs:     []error{errMarker, errAlwaysFails},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown},
		wantKinds:    map[string]*KindError{"b": ErrPanic},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": errMarker.Error()},
	}
}

func failedKindRow() runRow {
	return runRow{
		name: "[Fail] When: an action returns a plain error ; Should: report ErrFailed in the error and the event",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), fails())
		},
		wantStatus:     StatusCompensated,
		wantKind:       ErrCompensated,
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("b"), failedB(), closed),
		wantErrs:       []error{errAlwaysFails, ErrFailed},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepFailed},
		wantKinds:      map[string]*KindError{"b": ErrFailed},
		wantFailed:     []string{"b"},
		wantErrText:    map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts:   map[string]int{"b": 3},
		wantPreviousIs: map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
	}
}

func panicKindRow() runRow {
	return runRow{
		name: "[Fail] When: an action panics with an error value ; Should: report ErrPanic and match the value",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), act("b", func(context.Context, tripState) error {
				panic(errAlwaysFails)
			}))
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantAttempts: map[string]int{"b": 1},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b"), closed),
		wantErrs:     []error{errAlwaysFails, ErrPanic},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown},
		wantKinds:    map[string]*KindError{"b": ErrPanic},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": "panic: " + errAlwaysFails.Error()},
	}
}

func siblingCauseRow() runRow {
	return runRow{
		name: "[Fail] When: a step fails beside a running sibling ; " +
			"Should: give the fixed stop cause, without its error",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), Parallel(fails(), waiting("c")))
		},
		wantStatus:         StatusCompensated,
		wantKind:           ErrCompensated,
		wantAttempts:       map[string]int{"b": 3, "c": 1},
		wantTrail:          afterA(together(startOf("b"), startOf("c"), failedB(), lostOf("c")), seq(closed)),
		wantErrs:           []error{errAlwaysFails, context.Canceled},
		wantSteps:          map[string]stepStatus{"a": stepSucceeded, "b": stepFailed, "c": stepUnknown},
		wantKinds:          map[string]*KindError{"b": ErrFailed, "c": ErrUnknown},
		wantFailed:         []string{"b", "c"},
		wantErrText:        map[string]string{"b": errAlwaysFails.Error(), "c": context.Canceled.Error()},
		wantPreviousIs:     map[string][]error{"b": {errAlwaysFails, errAlwaysFails}},
		wantNotKinds:       map[string]*KindError{"c": ErrFailed},
		wantNotCauseIs:     map[string]error{"c": errAlwaysFails},
		wantCauseText:      map[string]string{"c": stopText},
		wantEventCauseText: map[string]string{"c": stopText},
		wantTextCount:      map[string]int{`step "b" did not succeed`: 1},
	}
}

func goexitHistoryRow() runRow {
	return runRow{
		name: "[Fail] When: an action ends by runtime.Goexit on attempt 2 ; Should: keep the attempts and attempt 1",
		steps: func(rec *recorder) Flow {
			count := counter()
			leaves := act("b", func(context.Context, tripState) error {
				if count() == 1 {
					return errFirst
				}

				runtime.Goexit()

				return nil
			})

			return Sequence(rec.step("a"), leaves)
		},
		wantStatus:     StatusCompensated,
		wantKind:       ErrCompensated,
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b"), closed),
		wantErrs:       []error{errGoexit, errFirst},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown},
		wantKinds:      map[string]*KindError{"b": ErrUnknown},
		wantFailed:     []string{"b"},
		wantErrText:    map[string]string{"b": goexitText},
		wantAttempts:   map[string]int{"b": 2},
		wantPreviousIs: map[string][]error{"b": {errFirst}},
	}
}

func droppedPredicateRow() runRow {
	return runRow{
		name: "[Fail] When: an Enabled method fails after a rejection ; Should: report it and not start the step",
		steps: func(rec *recorder) Flow {
			late := func(context.Context, tripState) bool {
				time.Sleep(pause)

				panic("late")
			}

			return Sequence(rec.step("a"), Parallel(rec.step("b"), rec.stepIf("c", late)))
		},
		script:       map[int]error{5: ErrConcurrentWriter},
		wantStatus:   noEndStatus,
		wantKind:     ErrConcurrentWriter,
		wantAttempts: map[string]int{"c": 0},
		wantErrs:     []error{ErrConcurrentWriter},
		wantNotErrs:  []error{ErrJournal},
		wantText:     []string{"panic: late"},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown, "c": stepNotStarted},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), startOf("b")),
		wantKinds:    map[string]*KindError{"c": ErrPanic},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": "panic: late"},

		wantUnrecorded: []string{"c"},
	}
}

func droppedOutcomeRow() runRow {
	return runRow{
		name: "[Fail] When: a sibling fails after a rejection ; Should: report its error and append nothing for it",
		steps: func(rec *recorder) Flow {
			late := once("c", func(context.Context, tripState) error {
				time.Sleep(pause)

				return errAlwaysFails
			})

			return Sequence(rec.step("a"), Parallel(rec.step("b"), late))
		},
		script:       map[int]error{6: ErrConcurrentWriter},
		wantStatus:   noEndStatus,
		wantKind:     ErrConcurrentWriter,
		wantAttempts: map[string]int{"c": 1},
		wantTrail:    afterA(together(startOf("b"), startOf("c"))),
		wantErrs:     []error{ErrConcurrentWriter, errAlwaysFails},
		wantNotErrs:  []error{ErrJournal},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown, "c": stepUnknown},
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},

		wantUnrecorded: []string{"c"},
		wantCauseText:  map[string]string{"c": "journal rejected an event"},
	}
}

func callerCauseRow() runRow {
	return runRow{
		name: "[Fail] When: the caller cancels with a cause while a step runs ; Should: match the cause and report it",
		steps: func(rec *recorder) Flow {
			waits := waiting("b")
			waits.RetryPolicy = NoRetry()

			return Sequence(rec.step("a"), waits)
		},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantAttempts: map[string]int{"b": 1},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), startOf("b"), lostOf("b"), closed),
		wantErrs:     []error{errStop, context.Canceled},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepUnknown},
		wantKinds:    map[string]*KindError{"b": ErrUnknown},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": context.Canceled.Error()},
		wantCauseIs:  map[string]error{"b": errStop},
	}
}

func callerKindRow(
	name string, wrapped error, status stepStatus, kind *KindError, last string,
) runRow {
	return runRow{
		name: name,
		steps: func(rec *recorder) Flow {
			wraps := once("b", func(context.Context, tripState) error {
				return fmt.Errorf("nested run: %w", wrapped)
			})

			return Sequence(rec.step("a"), wraps)
		},
		wantStatus:  StatusCompensated,
		wantKind:    ErrCompensated,
		wantErrs:    []error{wrapped},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "b": status},
		wantKinds:   map[string]*KindError{"b": kind},
		wantFailed:  []string{"b"},
		wantErrText: map[string]string{"b": "nested run: " + wrapped.Error()},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("b"), last, closed),

		wantAttempts: map[string]int{"b": 1},
	}
}

func callerInvalidStateRow() runRow {
	return callerKindRow(
		"[Fail] When: an action returns an error that wraps ErrInvalidState ; "+
			"Should: report ErrFailed, not ErrInvalidState",
		ErrInvalidState, stepFailed, ErrFailed, failedB(),
	)
}

func callerJournalRow() runRow {
	return callerKindRow(
		"[Fail] When: an action returns an error that wraps ErrJournal ; Should: report ErrFailed, not ErrJournal",
		ErrJournal, stepFailed, ErrFailed, failedB(),
	)
}

func callerPanicRow() runRow {
	return callerKindRow(
		"[Fail] When: an action returns an error that wraps a PanicError ; Should: report ErrUnknown, not ErrPanic",
		&PanicError{Value: "nested"}, stepUnknown, ErrUnknown, lostOf("b"),
	)
}

func nestedRow(name string, inner Flow, attempts int, errText string, wantErrs []error) runRow {
	return runRow{
		name: name,
		steps: func(rec *recorder) Flow {
			nests := act("b", func(ctx context.Context, _ tripState) error {
				s, err := New[tripState]("inner", inner)
				if err != nil {
					return err
				}

				return s.Run(ctx, &tripState{})
			})
			nests.RetryPolicy = RetryPolicy{Attempts: attempts}

			return Sequence(rec.step("a"), nests)
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantErrs:     wantErrs,
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepFailed},
		wantKinds:    map[string]*KindError{"b": ErrFailed},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": `step "x" did not succeed after 1 attempts: ` + errText},
		wantAttempts: map[string]int{"b": 1},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), startOf("b"), failedB(), closed),
	}
}

func nestedPermanentRow() runRow {
	return nestedRow(
		"[Fail] When: an action returns a nested run that holds a permanent error ; Should: stop after 1 attempt",
		act("x", func(context.Context, tripState) error { return Permanent(errAlwaysFails) }),
		3, errAlwaysFails.Error(), []error{errAlwaysFails, ErrFailed},
	)
}

const nestedStateText = `step "x" did not succeed after 1 attempts: sagas: invalid state: Rate: `

func nestedMiddlewareRow() runRow {
	return runRow{
		name: "[Fail] When: step middleware returns a nested run error without calling the step ; " +
			"Should: label the outer step ErrFailed, not the inner kind",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.step("a"), act("b", okAction))
		},
		opts:         []Option{WithMiddleware(nestingMiddleware())},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantErrs:     []error{ErrInvalidState, ErrCompensated, ErrFailed},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepFailed},
		wantKinds:    map[string]*KindError{"b": ErrFailed},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": nestedStateText},
		wantAttempts: map[string]int{"b": 3},
		wantTrail:    seq(opened, startOf("a"), doneOf("a"), startOf("b"), failedB(), closed),

		wantPreviousIs: map[string][]error{"b": {ErrInvalidState, ErrInvalidState}},
	}
}

func nestingMiddleware() MiddlewareFunc {
	return func(ctx context.Context, info CallInfo, call Call) error {
		if info.Step != "b" {
			return call(ctx)
		}

		inner, err := New[tripState]("inner", producingRate("x", makeOf(Rate(math.NaN()))))
		if err != nil {
			return err
		}

		return inner.Run(ctx, &tripState{})
	}
}

func failedRun(t *testing.T, script map[int]error, stepErr error) *RunError {
	t.Helper()

	second := once("b", func(context.Context, tripState) error { return stepErr })

	s, err := New[tripState](tripName, Sequence(once("a", okAction), second), WithJournal(newScriptJournal(script)))
	require.NoError(t, err)

	runErr, ok := errors.AsType[*RunError](s.Run(t.Context(), &tripState{}))
	require.True(t, ok, "the run did not return a RunError")

	return runErr
}

func Test_RunError_Error(t *testing.T) {
	t.Parallel()

	bFailed := `sagas: step "b" did not succeed after 1 attempts: ` + errAlwaysFails.Error()
	journalDown := ErrJournal.Error() + ": " + errJournalDown.Error()

	tests := []struct {
		name    string
		script  map[int]error
		stepErr error
		want    string
	}{
		{
			name:    "[Success] When: a step failed ; Should: read the kind, then the step error",
			stepErr: errAlwaysFails,
			want:    "sagas: compensated: " + bFailed,
		},
		{
			name:    "[Success] When: a step and the journal failed ; Should: join the texts with a semicolon",
			script:  map[int]error{5: errJournalDown},
			stepErr: errAlwaysFails,
			want:    "sagas: compensated: " + bFailed + "; " + journalDown,
		},
		{
			name:   "[Success] When: only the journal failed ; Should: read the kind, then the journal error",
			script: map[int]error{3: errJournalDown},
			want:   "sagas: compensated: " + journalDown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, failedRun(t, tt.script, tt.stepErr).Error())
		})
	}
}

func Test_RunError_Error_noCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  RunError
		want string
	}{
		{
			name: "[Success] When: nothing caused the end ; Should: read only the kind",
			err:  RunError{Kind: ErrIncomplete},
			want: "sagas: incomplete",
		},
		{
			name: "[Success] When: the error is the zero value ; Should: return an empty message without a panic",
			err:  RunError{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func Test_RunError_Is(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		script  map[int]error
		stepErr error
		target  error
		want    bool
	}{
		{
			name:    "[Success] When: the target is the Kind ; Should: match",
			stepErr: errAlwaysFails,
			target:  ErrCompensated,
			want:    true,
		},
		{
			name:    "[Success] When: a step error holds the target ; Should: match through the chain",
			stepErr: errAlwaysFails,
			target:  errAlwaysFails,
			want:    true,
		},
		{
			name:   "[Success] When: a journal error holds the target ; Should: match through the chain",
			script: map[int]error{3: errJournalDown},
			target: ErrJournal,
			want:   true,
		},
		{
			name:   "[Success] When: a journal rejection is the target ; Should: match through the chain",
			script: map[int]error{3: ErrConcurrentWriter},
			target: ErrConcurrentWriter,
			want:   true,
		},
		{
			name:    "[Fail] When: the target is another Kind ; Should: not match",
			stepErr: errAlwaysFails,
			target:  ErrCompensationFailed,
		},
		{
			name:    "[Fail] When: a caller error has the same text as the Kind ; Should: not match",
			stepErr: errAlwaysFails,
			target:  errLookalikeCompensated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, errors.Is(failedRun(t, tt.script, tt.stepErr), tt.target))
		})
	}
}

func Test_RunError_StepOf(t *testing.T) {
	t.Parallel()

	failedA := &Error{Kind: ErrFailed, Step: "a"}
	unknownB := &Error{Kind: ErrUnknown, Step: "b", Compensation: true}
	failedC := &Error{Kind: ErrFailed, Step: "c", Compensation: true}
	wrapsGrace := &Error{Kind: ErrFailed, Step: "d", Err: fmt.Errorf("late: %w", ErrGraceEnded)}

	tests := []struct {
		name  string
		steps []*Error
		kind  *KindError
		want  *Error
	}{
		{
			name:  "[Success] When: one step error has the kind ; Should: return it",
			steps: []*Error{failedA, unknownB},
			kind:  ErrUnknown,
			want:  unknownB,
		},
		{
			name:  "[Fail] When: no step error has the kind ; Should: return nil",
			steps: []*Error{failedA, unknownB},
			kind:  ErrPanic,
		},
		{
			name:  "[Fail] When: a step error wraps the kind in Err but has another Kind ; Should: return nil",
			steps: []*Error{wrapsGrace},
			kind:  ErrGraceEnded,
		},
		{
			name:  "[Success] When: two step errors have the kind ; Should: return the first",
			steps: []*Error{unknownB, failedA, failedC},
			kind:  ErrFailed,
			want:  failedA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runErr := &RunError{Kind: ErrCompensationFailed, Steps: tt.steps}

			assert.Equal(t, tt.want, runErr.StepOf(tt.kind))
		})
	}
}

func Test_RunError_Unwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		script     map[int]error
		stepErr    error
		wantLen    int
		wantSteps  []string
		wantCauses []error
	}{
		{
			name:      "[Success] When: only a step failed ; Should: return its error",
			stepErr:   errAlwaysFails,
			wantLen:   1,
			wantSteps: []string{"b"},
		},
		{
			name:       "[Success] When: only the journal failed ; Should: return the journal error",
			script:     map[int]error{3: errJournalDown},
			wantLen:    1,
			wantCauses: []error{ErrJournal, errJournalDown},
		},
		{
			name:       "[Success] When: a step failed and the journal rejected ; Should: return the step first",
			script:     map[int]error{5: ErrConcurrentWriter},
			stepErr:    errAlwaysFails,
			wantLen:    2,
			wantSteps:  []string{"b"},
			wantCauses: []error{ErrConcurrentWriter},
		},
		{
			name:       "[Success] When: a step failed and the journal failed ; Should: return the step first",
			script:     map[int]error{5: errJournalDown},
			stepErr:    errAlwaysFails,
			wantLen:    2,
			wantSteps:  []string{"b"},
			wantCauses: []error{ErrJournal, errJournalDown},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runErr := failedRun(t, tt.script, tt.stepErr)
			got := runErr.Unwrap()

			require.Len(t, got, tt.wantLen)

			for i, step := range tt.wantSteps {
				stepFailure, ok := errors.AsType[*Error](got[i])
				require.True(t, ok, "%v", got[i])
				assert.Equal(t, step, stepFailure.Step)
			}

			for _, want := range tt.wantCauses {
				require.ErrorIs(t, got[len(got)-1], want)
			}
		})
	}
}

func Test_RunError_Unwrap_empty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  RunError
	}{
		{name: "[Success] When: nothing is set ; Should: return no errors", err: RunError{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Empty(t, tt.err.Unwrap())
		})
	}
}
