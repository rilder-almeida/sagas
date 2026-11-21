package sagas

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	infoSaga      = "booking"
	infoExecution = "exec-1"
)

func Test_CallInfo_IdempotencyKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info CallInfo
		want string
	}{
		{
			name: "[Success] When: an execute call of a step ; Should: join the execution and step",
			info: CallInfo{Saga: infoSaga, Execution: infoExecution, Step: secondStep, Phase: PhaseExecute, Attempt: 1},
			want: infoExecution + "/" + secondStep,
		},
		{
			name: "[Success] When: a later compensate attempt of the same step ; Should: return the same key",
			info: CallInfo{
				Saga:      infoSaga,
				Execution: infoExecution,
				Step:      secondStep,
				Phase:     PhaseCompensate,
				Attempt:   3,
			},
			want: infoExecution + "/" + secondStep,
		},
		{
			name: "[Success] When: another execution of the step ; Should: return a different key",
			info: CallInfo{Saga: infoSaga, Execution: "exec-2", Step: secondStep, Phase: PhaseExecute, Attempt: 1},
			want: "exec-2/" + secondStep,
		},
		{
			name: "[Fail] When: the zero CallInfo ; Should: return only the separator",
			info: CallInfo{},
			want: "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.info.IdempotencyKey())
		})
	}
}

func Test_IdempotencyKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		inCall bool
		phase  Phase
		want   string
	}{
		{
			name:   "[Success] When: inside an execute call ; Should: return the execution and step",
			inCall: true,
			phase:  PhaseExecute,
			want:   infoExecution + "/" + secondStep,
		},
		{
			name:   "[Success] When: inside a pivot call ; Should: return the same key",
			inCall: true,
			phase:  PhasePivot,
			want:   infoExecution + "/" + secondStep,
		},
		{
			name:   "[Success] When: inside a compensate call ; Should: return the same key",
			inCall: true,
			phase:  PhaseCompensate,
			want:   infoExecution + "/" + secondStep,
		},
		{
			name:   "[Fail] When: outside any call ; Should: return an empty key",
			inCall: false,
			phase:  "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := IdempotencyKey(t.Context())

			if tt.inCall {
				spec := &callSpec{saga: infoSaga, step: secondStep, phase: tt.phase, retry: RetryPolicy{Attempts: 1}}
				call := func(ctx context.Context, _ *pipeState) error {
					got = IdempotencyKey(ctx)

					return nil
				}

				out := pipeline(spec, call)(t.Context(), &callMeta{execution: infoExecution}, baseOf(t, pipeState{}))
				require.NoError(t, out.err)
			}

			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_callMeta_closeAttempt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		meta         *callMeta
		err          error
		wantPrevious []error
		wantCause    error
		wantAfter    []error
	}{
		{
			name:         "[Success] When: the attempt failed ; Should: record it and clear the attempt",
			meta:         &callMeta{cause: nil, label: ErrPanic},
			err:          errBoom,
			wantPrevious: []error{errBoom},
			wantAfter:    []error{errBoom, errC},
		},
		{
			name:         "[Success] When: an error was replaced and a cause is set ; Should: record both",
			meta:         &callMeta{replaced: []error{errCall}, cause: errStop},
			err:          errBoom,
			wantPrevious: []error{errCall, errBoom},
			wantCause:    errStop,
			wantAfter:    []error{errCall, errBoom, errC},
		},
		{
			name:         "[Success] When: an earlier attempt is recorded ; Should: keep it first",
			meta:         &callMeta{previous: []error{errA}},
			err:          errBoom,
			wantPrevious: []error{errA, errBoom},
			wantAfter:    []error{errA, errBoom, errC},
		},
		{
			name: "[Success] When: the meta is nil ; Should: do nothing",
			meta: nil,
			err:  errBoom,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.meta.closeAttempt(tt.err)

			history := tt.meta.history()
			assertPrevious(t, history, tt.wantPrevious)
			assertLastCause(t, history, tt.wantCause)

			tt.meta.closeAttempt(errC)

			again := tt.meta.history()
			assertPrevious(t, again, tt.wantAfter)
			assertLastCause(t, again, nil)

			assert.Nil(t, pipelineLabel(t, tt.meta))
		})
	}
}

func pipelineLabel(tb testing.TB, m *callMeta) *KindError {
	tb.Helper()

	if m == nil {
		return nil
	}

	spec := &callSpec{
		saga: infoSaga, step: secondStep, phase: PhaseExecute, retry: RetryPolicy{Attempts: 1},
		middleware: []Middleware{MiddlewareFunc(replacing)},
	}
	call := func(context.Context, *pipeState) error { return errBoom }

	out := pipeline(spec, call)(tb.Context(), m, baseOf(tb, pipeState{}))
	require.ErrorIs(tb, out.err, errReplaced)

	return out.label
}

func assertLastCause(t *testing.T, history []error, want error) {
	t.Helper()

	var last error
	if len(history) > 0 {
		last = history[len(history)-1]
	}

	if want == nil {
		require.NotErrorIs(t, last, errStop)

		return
	}

	require.ErrorIs(t, last, want)
}

func Test_callMeta_history(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta *callMeta
		want []error
	}{
		{
			name: "[Success] When: nothing is recorded ; Should: return no errors",
			meta: &callMeta{},
		},
		{
			name: "[Success] When: earlier attempts and a replaced error exist ; Should: list the attempts first",
			meta: &callMeta{previous: []error{errA}, replaced: []error{errB}},
			want: []error{errA, errB},
		},
		{
			name: "[Success] When: the meta is nil ; Should: return no errors",
			meta: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assertPrevious(t, tt.meta.history(), tt.want)
		})
	}
}

func Test_callMeta_mark(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		meta  *callMeta
		label *KindError
		want  *KindError
	}{
		{
			name: "[Success] When: the meta has no label ; Should: set it",
			meta: &callMeta{}, label: ErrPanic, want: ErrPanic,
		},
		{
			name: "[Success] When: the meta has a label ; Should: replace it",
			meta: &callMeta{label: ErrPanic}, label: ErrConflict, want: ErrConflict,
		},
		{
			name: "[Success] When: the meta has a label and the new one is nil ; Should: clear it",
			meta: &callMeta{label: ErrPanic}, label: nil, want: nil,
		},
		{name: "[Success] When: the meta is nil ; Should: change nothing", meta: nil, label: ErrPanic, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.meta.mark(tt.label)

			assert.Same(t, tt.want, pipelineLabel(t, tt.meta))
		})
	}
}

func Test_callMeta_replace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta *callMeta
		want []error
	}{
		{name: "[Success] When: nothing was replaced ; Should: record it", meta: &callMeta{}, want: []error{errA}},
		{
			name: "[Success] When: an error was replaced ; Should: keep it first",
			meta: &callMeta{replaced: []error{errB}},
			want: []error{errB, errA},
		},
		{
			name: "[Success] When: an earlier attempt is recorded ; Should: list the replaced error after it",
			meta: &callMeta{previous: []error{errC}},
			want: []error{errC, errA},
		},
		{name: "[Success] When: the meta is nil ; Should: not panic", meta: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.meta.replace(errA)

			assertPrevious(t, tt.meta.history(), tt.want)
		})
	}
}

func Test_callMeta_end(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		meta    *callMeta
		want    error
		wantNot error
		wantLen int
	}{
		{
			name: "[Success] When: no cause is set ; Should: set it",
			meta: &callMeta{}, want: errA, wantNot: errB, wantLen: 1,
		},
		{
			name: "[Success] When: a cause is set ; Should: keep the first",
			meta: &callMeta{cause: errB}, want: errB, wantNot: errA, wantLen: 1,
		},
		{
			name: "[Success] When: the meta is nil ; Should: leave it alone",
			meta: nil, want: nil, wantNot: errA, wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.meta.end(errA)
			tt.meta.closeAttempt(errBoom)

			history := tt.meta.history()
			require.Len(t, history, tt.wantLen)
			assertLastCause(t, history, tt.want)
			require.NotErrorIs(t, errors.Join(history...), tt.wantNot)
		})
	}
}
