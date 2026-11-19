package sagas

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	infoSaga      = "booking"
	infoExecution = "exec-1"
)

func infoInCall(t *testing.T, phase Phase, attempt int) (StepInfo, bool) {
	t.Helper()

	var (
		got StepInfo
		ok  bool
	)

	ctx := withMeta(t.Context(), &callMeta{execution: infoExecution, attempt: attempt})
	handler := withStepInfo(infoSaga, secondStep, phase)(func(ctx context.Context) error {
		got, ok = Info(ctx)

		return nil
	})

	require.NoError(t, handler(ctx))

	return got, ok
}

func Test_Info(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		inCall  bool
		phase   Phase
		attempt int
		want    StepInfo
		wantOK  bool
	}{
		{
			name:    "[Success] When: inside an action call ; Should: return every field",
			inCall:  true,
			phase:   PhaseAction,
			attempt: 2,
			want: StepInfo{
				Saga: infoSaga, Execution: infoExecution, Step: secondStep,
				Phase: PhaseAction, Attempt: 2, Key: infoExecution + "/" + secondStep,
			},
			wantOK: true,
		},
		{
			name:    "[Success] When: inside a compensation call ; Should: return the same key as the action",
			inCall:  true,
			phase:   PhaseCompensation,
			attempt: 1,
			want: StepInfo{
				Saga: infoSaga, Execution: infoExecution, Step: secondStep,
				Phase: PhaseCompensation, Attempt: 1, Key: infoExecution + "/" + secondStep,
			},
			wantOK: true,
		},
		{
			name:   "[Fail] When: outside any call ; Should: return the zero value and false",
			inCall: false,
			want:   StepInfo{},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := Info(t.Context())
			if tt.inCall {
				got, ok = infoInCall(t, tt.phase, tt.attempt)
			}

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func Test_Key(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		inCall bool
		want   string
	}{
		{
			name:   "[Success] When: inside a call ; Should: return the execution and step",
			inCall: true,
			want:   infoExecution + "/" + secondStep,
		},
		{
			name:   "[Fail] When: outside any call ; Should: return an empty key",
			inCall: false,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got string

			ctx := t.Context()
			if tt.inCall {
				ctx = withMeta(ctx, &callMeta{execution: infoExecution, attempt: 1})
			}

			handler := Handler(func(ctx context.Context) error {
				got = Key(ctx)

				return nil
			})
			if tt.inCall {
				handler = withStepInfo(infoSaga, secondStep, PhaseAction)(handler)
			}

			require.NoError(t, handler(ctx))
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
	}{
		{
			name:         "[Success] When: the attempt failed ; Should: record it and clear the attempt",
			meta:         &callMeta{cause: nil, label: ErrPanic},
			err:          errBoom,
			wantPrevious: []error{errBoom},
		},
		{
			name:         "[Success] When: an error was replaced and a cause is set ; Should: record both",
			meta:         &callMeta{replaced: []error{errCall}, cause: errStop},
			err:          errBoom,
			wantPrevious: []error{errCall, errBoom},
		},
		{
			name:         "[Success] When: an earlier attempt is recorded ; Should: keep it first",
			meta:         &callMeta{previous: []error{errA}},
			err:          errBoom,
			wantPrevious: []error{errA, errBoom},
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

			assertPrevious(t, tt.meta.history(), tt.wantPrevious)

			if tt.meta != nil {
				require.NoError(t, tt.meta.cause)
				assert.Nil(t, tt.meta.label)
				assert.Empty(t, tt.meta.replaced)
			}
		})
	}
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
		name string
		meta *callMeta
		want *KindError
	}{
		{name: "[Success] When: the meta has no label ; Should: set it", meta: &callMeta{}, want: ErrPanic},
		{name: "[Success] When: the meta has a label ; Should: replace it", meta: &callMeta{label: ErrPanic}},
		{name: "[Success] When: the meta is nil ; Should: change nothing", meta: nil, want: ErrPanic},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.meta.mark(tt.want)

			if tt.meta != nil {
				assert.Same(t, tt.want, tt.meta.label)
			}
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
		{name: "[Success] When: the meta is nil ; Should: not panic", meta: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.meta.replace(errA)

			if tt.meta != nil {
				assertPrevious(t, tt.meta.replaced, tt.want)
			}
		})
	}
}

func Test_callMeta_end(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta *callMeta
		want error
	}{
		{name: "[Success] When: no cause is set ; Should: set it", meta: &callMeta{}, want: errA},
		{name: "[Success] When: a cause is set ; Should: keep the first", meta: &callMeta{cause: errB}, want: errB},
		{name: "[Success] When: the meta is nil ; Should: leave it alone", meta: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.meta.end(errA)

			if tt.meta != nil {
				assert.Same(t, tt.want, tt.meta.cause)
			}
		})
	}
}
