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
