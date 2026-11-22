package sagastest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rilder-almeida/sagas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

type probeState struct {
	N int
}

func (probeState) ExecutionID() string { return "run-1" }

type probeStep struct {
	sagas.Step

	calls atomic.Int32
	err   error
}

func newProbe(name string, retry sagas.RetryPolicy, err error) *probeStep {
	p := &probeStep{err: err}
	p.Name, p.Retry = name, retry

	return p
}

func (p *probeStep) Execute(context.Context, *probeState) error {
	p.calls.Add(1)

	return p.err
}

func fastRetry() sagas.RetryPolicy {
	return sagas.RetryPolicy{Attempts: 2, Initial: time.Millisecond, NoJitter: true}
}

func runProbe(t *testing.T, retry sagas.RetryPolicy, mw sagas.Middleware) (int, error) {
	t.Helper()

	probe := newProbe("probe", retry, nil)
	saga, err := sagas.New[probeState]("probe-saga", probe, sagas.WithMiddleware(mw))
	require.NoError(t, err)

	err = saga.Run(context.Background(), &probeState{})

	return int(probe.calls.Load()), err
}

func Test_Fail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		step      string
		wantErr   error
		wantCalls int
	}{
		{
			name:      "[Success] When: the step name matches ; Should: return the error and skip the call",
			step:      "probe",
			wantErr:   errBoom,
			wantCalls: 0,
		},
		{
			name:      "[Success] When: another step name is given ; Should: leave the run untouched",
			step:      "other",
			wantCalls: 1,
		},
		{
			name:      "[Success] When: the step name is misspelled ; Should: never fire",
			step:      "probe ",
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := runProbe(t, sagas.NoRetry(), Fail(tt.step, errBoom))

			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}

			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func Test_Unknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		step        string
		wantUnknown bool
		wantCalls   int
	}{
		{
			name:        "[Success] When: the step name matches ; Should: run the call once and end unknown",
			step:        "probe",
			wantUnknown: true,
			wantCalls:   1,
		},
		{
			name:      "[Success] When: another step name is given ; Should: leave the run untouched",
			step:      "other",
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := runProbe(t, sagas.NoRetry(), Unknown(tt.step))

			if tt.wantUnknown {
				require.Error(t, err)
				require.ErrorIs(t, err, sagas.ErrUnknown)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func Test_Panic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		step      string
		wantPanic bool
		wantCalls int
	}{
		{
			name:      "[Success] When: the step name matches ; Should: run the call once and report a panic error",
			step:      "probe",
			wantPanic: true,
			wantCalls: 1,
		},
		{
			name:      "[Success] When: another step name is given ; Should: leave the run untouched",
			step:      "other",
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := runProbe(t, fastRetry(), Panic(tt.step))

			if tt.wantPanic {
				var panicked *sagas.PanicError

				require.ErrorAs(t, err, &panicked)
				require.ErrorIs(t, err, sagas.ErrPanic)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func Test_OnPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		phase     sagas.Phase
		wantErr   bool
		wantCalls int
	}{
		{
			name:      "[Success] When: the fault targets the execute phase ; Should: fail the execute call",
			phase:     sagas.PhaseExecute,
			wantErr:   true,
			wantCalls: 0,
		},
		{
			name:      "[Success] When: the fault targets the compensate phase ; Should: leave the execute phase alone",
			phase:     sagas.PhaseCompensate,
			wantCalls: 1,
		},
		{
			name:      "[Success] When: the fault targets the pivot phase ; Should: leave the execute phase alone",
			phase:     sagas.PhasePivot,
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := runProbe(t, sagas.NoRetry(), Fail("probe", errBoom, OnPhase(tt.phase)))

			if tt.wantErr {
				require.ErrorIs(t, err, errBoom)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func Test_OnAttempts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		attempts  []int
		wantErr   bool
		wantCalls int
	}{
		{
			name:      "[Success] When: only attempt 1 is listed ; Should: fail attempt 1 and pass the retry",
			attempts:  []int{1},
			wantCalls: 1,
		},
		{
			name:      "[Fail] When: attempts 1 and 2 are listed ; Should: fail both and never run the call",
			attempts:  []int{1, 2},
			wantErr:   true,
			wantCalls: 0,
		},
		{
			name:      "[Success] When: only attempt 3 is listed ; Should: never fire in a two-attempt run",
			attempts:  []int{3},
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := runProbe(t, fastRetry(), Fail("probe", errBoom, OnAttempts(tt.attempts...)))

			if tt.wantErr {
				require.ErrorIs(t, err, errBoom)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}
