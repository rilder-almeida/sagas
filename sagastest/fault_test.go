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

var (
	errBoom = errors.New("boom")
	errTail = errors.New("tail")
)

const runID = "run-1"

type count int

type probeState struct {
	N  count
	ID string `sagas:"id"`
}

type probeStep struct {
	sagas.Step[probeState, sagas.None]

	calls atomic.Int32
	err   error
}

func plain(name string) *probeStep { return newProbe(name, sagas.NoRetry(), nil) }

func newProbe(name string, retry sagas.RetryPolicy, err error) *probeStep {
	p := &probeStep{err: err}
	p.Name, p.RetryPolicy = name, retry

	return p
}

func (p *probeStep) Execute(context.Context, probeState) error {
	p.calls.Add(1)

	return p.err
}

type undoProbe struct {
	sagas.Step[probeState, sagas.None]

	execs atomic.Int32
	undos atomic.Int32
	err   error
}

func newUndo(name string, err error) *undoProbe {
	p := &undoProbe{err: err}
	p.Name, p.RetryPolicy = name, sagas.NoRetry()
	p.RecoveryPolicy = sagas.RecoveryPolicy{Attempts: 1}

	return p
}

func (p *undoProbe) Execute(context.Context, probeState) error {
	p.execs.Add(1)

	return p.err
}

func (p *undoProbe) Compensate(context.Context, probeState) error {
	p.undos.Add(1)

	return nil
}

type pivotProbe struct {
	sagas.Pivot[probeState, sagas.None]

	pivots atomic.Int32
}

func newPivot(name string) *pivotProbe {
	p := &pivotProbe{}
	p.Name, p.RetryPolicy = name, sagas.NoRetry()

	return p
}

func (p *pivotProbe) Execute(context.Context, probeState) error {
	p.pivots.Add(1)

	return nil
}

func fastRetry() sagas.RetryPolicy {
	return sagas.RetryPolicy{Attempts: 2, Initial: time.Millisecond, NoJitter: true}
}

func runFlow(t *testing.T, flow sagas.Flow, mw sagas.Middleware) error {
	t.Helper()

	saga, err := sagas.New[probeState]("probe-saga", flow, sagas.WithMiddleware(mw))
	require.NoError(t, err)

	return saga.Run(context.Background(), &probeState{ID: runID})
}

func runProbe(t *testing.T, retry sagas.RetryPolicy, probeErr error, mw sagas.Middleware) (int, error) {
	t.Helper()

	probe := newProbe(probeName, retry, probeErr)
	err := runFlow(t, probe, mw)

	return int(probe.calls.Load()), err
}

type built struct {
	flow  sagas.Flow
	count func() int
}

type fixture func() built

const (
	probeName = "probe"
	otherName = "other"
)

func executeFixture() built {
	p := newProbe(probeName, sagas.NoRetry(), nil)

	return built{flow: p, count: func() int { return int(p.calls.Load()) }}
}

func compensateFixture() built {
	u := newUndo(probeName, nil)
	tail := newProbe("tail", sagas.NoRetry(), errTail)

	return built{flow: sagas.Sequence(u, tail), count: func() int { return int(u.undos.Load()) }}
}

func pivotFixture() built {
	p := newPivot(probeName)

	return built{flow: p, count: func() int { return int(p.pivots.Load()) }}
}

func assertErr(t *testing.T, err, want error) {
	t.Helper()

	if want == nil {
		require.NoError(t, err)

		return
	}

	require.ErrorIs(t, err, want)
}

func Test_Fail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		step      string
		inject    error
		wantErr   error
		wantPanic bool
		wantCalls int
	}{
		{
			name:      "[Fail] When: the step name matches ; Should: return the error and skip the call",
			step:      probeName,
			inject:    errBoom,
			wantErr:   errBoom,
			wantCalls: 0,
		},
		{
			name:      "[Success] When: another step name is given ; Should: leave the run untouched",
			step:      otherName,
			inject:    errBoom,
			wantCalls: 1,
		},
		{
			name:      "[Success] When: the step name is misspelled ; Should: never fire",
			step:      "probe ",
			inject:    errBoom,
			wantCalls: 1,
		},
		{
			name:      "[Fail] When: the error is nil ; Should: panic at construction",
			step:      probeName,
			wantPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.wantPanic {
				assert.PanicsWithValue(t, "sagastest: Fail needs a non-nil error", func() { Fail(tt.step, tt.inject) })

				return
			}

			calls, err := runProbe(t, sagas.NoRetry(), nil, Fail(tt.step, tt.inject))

			assertErr(t, err, tt.wantErr)
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func Test_Unknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		step      string
		opts      []FaultOption
		retry     sagas.RetryPolicy
		probeErr  error
		wantErr   error
		wantNot   error
		wantCalls int
	}{
		{
			name:      "[Fail] When: the step name matches ; Should: run the call once and end unknown",
			step:      probeName,
			retry:     sagas.NoRetry(),
			wantErr:   sagas.ErrUnknown,
			wantCalls: 1,
		},
		{
			name:      "[Success] When: another step name is given ; Should: leave the run untouched",
			step:      otherName,
			retry:     sagas.NoRetry(),
			wantCalls: 1,
		},
		{
			name:      "[Fail] When: the call returns an error ; Should: keep that error and report no deadline",
			step:      probeName,
			retry:     sagas.NoRetry(),
			probeErr:  errBoom,
			wantErr:   errBoom,
			wantNot:   context.DeadlineExceeded,
			wantCalls: 1,
		},
		{
			name:      "[Fail] When: it matches every attempt of two ; Should: retry the lost reply and end unknown",
			step:      probeName,
			retry:     fastRetry(),
			wantErr:   sagas.ErrUnknown,
			wantCalls: 2,
		},
		{
			name:      "[Success] When: it matches only attempt 1 of two ; Should: retry once and succeed",
			step:      probeName,
			opts:      []FaultOption{OnAttempts(1)},
			retry:     fastRetry(),
			wantCalls: 2,
		},
		{
			name:      "[Success] When: another step name is given with two attempts ; Should: run the call once",
			step:      otherName,
			retry:     fastRetry(),
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := runProbe(t, tt.retry, tt.probeErr, Unknown(tt.step, tt.opts...))

			assertErr(t, err, tt.wantErr)

			if tt.wantNot != nil {
				require.NotErrorIs(t, err, tt.wantNot)
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
		retry     sagas.RetryPolicy
		probeErr  error
		wantErr   error
		wantPanic bool
		wantCalls int
	}{
		{
			name:      "[Fail] When: the step name matches ; Should: run the call once and report a panic error",
			step:      probeName,
			retry:     sagas.NoRetry(),
			wantErr:   sagas.ErrPanic,
			wantPanic: true,
			wantCalls: 1,
		},
		{
			name:      "[Success] When: another step name is given ; Should: leave the run untouched",
			step:      otherName,
			retry:     sagas.NoRetry(),
			wantCalls: 1,
		},
		{
			name:      "[Fail] When: the matched call returns an error ; Should: keep that error and not panic",
			step:      probeName,
			retry:     sagas.NoRetry(),
			probeErr:  errBoom,
			wantErr:   errBoom,
			wantCalls: 1,
		},
		{
			name:      "[Fail] When: the step name matches with two attempts ; Should: not retry the panic",
			step:      probeName,
			retry:     fastRetry(),
			wantErr:   sagas.ErrPanic,
			wantPanic: true,
			wantCalls: 1,
		},
		{
			name:      "[Fail] When: the call errs with two attempts ; Should: retry the error and not panic",
			step:      probeName,
			retry:     fastRetry(),
			probeErr:  errBoom,
			wantErr:   errBoom,
			wantCalls: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := runProbe(t, tt.retry, tt.probeErr, Panic(tt.step))

			assertErr(t, err, tt.wantErr)

			var panicked *sagas.PanicError

			assert.Equal(t, tt.wantPanic, errors.As(err, &panicked))
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func Test_OnPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		target    fixture
		phase     sagas.Phase
		wantErr   error
		wantCalls int
	}{
		{
			name:      "[Fail] When: the fault targets execute on an execute step ; Should: fail the execute call",
			target:    executeFixture,
			phase:     sagas.PhaseExecute,
			wantErr:   errBoom,
			wantCalls: 0,
		},
		{
			name:      "[Success] When: the fault targets compensate on an execute step ; Should: leave execute alone",
			target:    executeFixture,
			phase:     sagas.PhaseCompensate,
			wantCalls: 1,
		},
		{
			name:      "[Success] When: the fault targets pivot on an execute step ; Should: leave execute alone",
			target:    executeFixture,
			phase:     sagas.PhasePivot,
			wantCalls: 1,
		},
		{
			name:      "[Fail] When: compensate is targeted on a compensable step ; Should: fail the compensation",
			target:    compensateFixture,
			phase:     sagas.PhaseCompensate,
			wantErr:   errBoom,
			wantCalls: 0,
		},
		{
			name:      "[Fail] When: pivot is targeted on a compensable step ; Should: leave the compensation alone",
			target:    compensateFixture,
			phase:     sagas.PhasePivot,
			wantErr:   errTail,
			wantCalls: 1,
		},
		{
			name:      "[Fail] When: the fault targets pivot on a pivot step ; Should: fail the pivot call",
			target:    pivotFixture,
			phase:     sagas.PhasePivot,
			wantErr:   errBoom,
			wantCalls: 0,
		},
		{
			name:      "[Success] When: the fault targets execute on a pivot step ; Should: leave the pivot alone",
			target:    pivotFixture,
			phase:     sagas.PhaseExecute,
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fx := tt.target()
			err := runFlow(t, fx.flow, Fail(probeName, errBoom, OnPhase(tt.phase)))

			assertErr(t, err, tt.wantErr)
			assert.Equal(t, tt.wantCalls, fx.count())
		})
	}
}

func Test_OnAttempts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		attempts  []int
		wantErr   error
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
			wantErr:   errBoom,
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

			calls, err := runProbe(t, fastRetry(), nil, Fail(probeName, errBoom, OnAttempts(tt.attempts...)))

			assertErr(t, err, tt.wantErr)
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}
