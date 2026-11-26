package sagas

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

func Test_Permanent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		in            error
		wantPermanent bool
		wantMessage   string
	}{
		{
			name:          "[Success] When: the error is plain ; Should: mark it and keep it in the chain",
			in:            errBoom,
			wantPermanent: true,
			wantMessage:   errBoom.Error(),
		},
		{
			name: "[Success] When: the error is nil ; Should: return nil",
			in:   nil,
		},
		{
			name:          "[Success] When: the error is already permanent ; Should: stay permanent, same message",
			in:            Permanent(errBoom),
			wantPermanent: true,
			wantMessage:   errBoom.Error(),
		},
		{
			name:          "[Success] When: a permanent error is wrapped ; Should: still detect it",
			in:            fmt.Errorf("x: %w", Permanent(errBoom)),
			wantPermanent: true,
			wantMessage:   "x: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Permanent(tt.in)

			require.ErrorIs(t, got, tt.in)
			assert.Equal(t, tt.wantPermanent, isPermanent(got))
			assert.Equal(t, tt.wantMessage, errText(got))
		})
	}
}

func Test_permanentError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "[Success] When: the cause is plain ; Should: report the cause's message",
			err:  errBoom,
			want: errBoom.Error(),
		},
		{
			name: "[Success] When: the cause is wrapped ; Should: report the wrapped message",
			err:  fmt.Errorf("x: %w", errBoom),
			want: "x: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, (&permanentError{err: tt.err}).Error())
		})
	}
}

func Test_permanentError_Unwrap(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("x: %w", errBoom)

	tests := []struct {
		name string
		err  error
	}{
		{
			name: "[Success] When: the cause is plain ; Should: return the cause",
			err:  errBoom,
		},
		{
			name: "[Success] When: the cause is wrapped ; Should: return the wrapped cause",
			err:  wrapped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.err, (&permanentError{err: tt.err}).Unwrap())
		})
	}
}

func Test_permanentError_Is(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target error
		want   bool
	}{
		{
			name:   "[Success] When: the target is the marker ; Should: match",
			target: errPermanent,
			want:   true,
		},
		{
			name:   "[Fail] When: the target is another error ; Should: not match",
			target: errBoom,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, (&permanentError{err: errBoom}).Is(tt.target))
		})
	}
}

func Test_backoff(t *testing.T) {
	t.Parallel()

	upper := func(n int64) int64 { return n - 1 }
	lower := func(int64) int64 { return 0 }

	tests := []struct {
		name    string
		policy  RetryPolicy
		attempt int
		r       func(int64) int64
		want    time.Duration
	}{
		{
			name:    "[Success] When: defaults and attempt 1 ; Should: wait the initial delay",
			policy:  RetryPolicy{NoJitter: true},
			attempt: 1,
			want:    200 * time.Millisecond,
		},
		{
			name:    "[Success] When: defaults and attempt 2 ; Should: double the delay",
			policy:  RetryPolicy{NoJitter: true},
			attempt: 2,
			want:    400 * time.Millisecond,
		},
		{
			name:    "[Success] When: defaults and attempt 3 ; Should: double again",
			policy:  RetryPolicy{NoJitter: true},
			attempt: 3,
			want:    800 * time.Millisecond,
		},
		{
			name:    "[Success] When: defaults and attempt 4 ; Should: reach 1.6s",
			policy:  RetryPolicy{NoJitter: true},
			attempt: 4,
			want:    1600 * time.Millisecond,
		},
		{
			name:    "[Success] When: growth passes Max ; Should: cap at Max",
			policy:  RetryPolicy{Initial: time.Second, Max: 3 * time.Second, NoJitter: true},
			attempt: 3,
			want:    3 * time.Second,
		},
		{
			name:    "[Success] When: Multiplier is 1 ; Should: keep the delay constant",
			policy:  RetryPolicy{Initial: time.Second, Multiplier: 1, NoJitter: true},
			attempt: 5,
			want:    time.Second,
		},
		{
			name:    "[Success] When: the power overflows a duration ; Should: cap at Max",
			policy:  RetryPolicy{Multiplier: 10, NoJitter: true},
			attempt: 1000,
			want:    DefaultMaxDelay,
		},
		{
			name:    "[Success] When: jitter draws the top of the range ; Should: wait just under the delay",
			policy:  RetryPolicy{},
			attempt: 2,
			r:       upper,
			want:    400*time.Millisecond - 1,
		},
		{
			name:    "[Success] When: the delay shrinks below 1ns ; Should: wait 1ns",
			policy:  RetryPolicy{Multiplier: 1e-10, NoJitter: true},
			attempt: 5,
			want:    1,
		},
		{
			name:    "[Success] When: the shrunk delay is jittered ; Should: not panic and draw zero",
			policy:  RetryPolicy{Multiplier: 1e-10},
			attempt: 5,
			r:       rand.Int64N,
			want:    0,
		},
		{
			name:    "[Success] When: Max is the largest duration and growth passes it ; Should: return Max",
			policy:  RetryPolicy{Max: math.MaxInt64, Multiplier: 10, NoJitter: true},
			attempt: 1000,
			want:    math.MaxInt64,
		},
		{
			name:    "[Success] When: Max is the largest duration and jitter draws the top ; Should: stay positive",
			policy:  RetryPolicy{Max: math.MaxInt64, Multiplier: 10},
			attempt: 1000,
			r:       upper,
			want:    math.MaxInt64 - 1,
		},
		{
			name:    "[Success] When: jitter draws zero ; Should: not wait",
			policy:  RetryPolicy{},
			attempt: 2,
			r:       lower,
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, backoff(tt.policy, tt.attempt, tt.r))
		})
	}
}

var (
	errA            = errors.New("a")
	errB            = errors.New("b")
	errC            = errors.New("c")
	errAttemptLimit = errors.New("attempt limit")
	errPanicked     = &PanicError{Value: "p"}
	errMarked       = Permanent(errA)
)

func scriptedCall(meta *callMeta, script []error, cancel context.CancelFunc, cancelOnCall int, seen *[]int) Call {
	calls := 0

	return func(ctx context.Context) error {
		calls++

		*seen = append(*seen, meta.attempt)

		if calls == cancelOnCall {
			cancel()
		}

		got := script[min(calls, len(script))-1]
		if !errors.Is(got, errAttemptLimit) {
			return got
		}

		child, stop := context.WithTimeout(ctx, time.Second)
		defer stop()

		<-child.Done()

		return child.Err()
	}
}

func assertRetryErr(t *testing.T, err, want error) {
	t.Helper()

	if want == nil {
		require.NoError(t, err)

		return
	}

	require.ErrorIs(t, err, want)
}

func Test_withRetry(t *testing.T) {
	t.Parallel()

	fast := RetryPolicy{NoJitter: true}

	tests := []struct {
		name         string
		policy       RetryPolicy
		script       []error
		cancelOnCall int
		cancelAt     time.Duration
		wantCalls    int
		wantElapsed  time.Duration
		wantErr      error
		wantPrevious []error
	}{
		{
			name:      "[Success] When: the first call succeeds ; Should: call once",
			policy:    fast,
			script:    []error{nil},
			wantCalls: 1,
		},
		{
			name:         "[Success] When: the third call succeeds ; Should: retry twice with growing waits",
			policy:       fast,
			script:       []error{errA, errB, nil},
			wantCalls:    3,
			wantElapsed:  600 * time.Millisecond,
			wantPrevious: []error{errA, errB},
		},
		{
			name:         "[Fail] When: every attempt fails ; Should: stop at the limit with the last error",
			policy:       fast,
			script:       []error{errA, errB, errC},
			wantCalls:    3,
			wantElapsed:  600 * time.Millisecond,
			wantErr:      errC,
			wantPrevious: []error{errA, errB},
		},
		{
			name: "[Success] When: the policy is fully set ; Should: follow its numbers",
			policy: RetryPolicy{
				Attempts:   4,
				Initial:    time.Second,
				Max:        5 * time.Second,
				Multiplier: 3,
				NoJitter:   true,
			},
			script:       []error{errA, errA, errA, nil},
			wantCalls:    4,
			wantElapsed:  9 * time.Second,
			wantPrevious: []error{errA, errA, errA},
		},
		{
			name:      "[Fail] When: the error is permanent ; Should: stop after one call",
			policy:    fast,
			script:    []error{errMarked, nil},
			wantCalls: 1,
			wantErr:   errMarked,
		},
		{
			name:      "[Fail] When: the error is a panic ; Should: stop after one call",
			policy:    fast,
			script:    []error{errPanicked, nil},
			wantCalls: 1,
			wantErr:   errPanicked,
		},
		{
			name:         "[Success] When: an attempt hits its own deadline ; Should: retry it",
			policy:       fast,
			script:       []error{errAttemptLimit, nil},
			wantCalls:    2,
			wantElapsed:  1200 * time.Millisecond,
			wantPrevious: []error{context.DeadlineExceeded},
		},
		{
			name:         "[Fail] When: the parent is done after attempt 1 ; Should: stop after one call",
			policy:       fast,
			script:       []error{errA, nil},
			cancelOnCall: 1,
			wantCalls:    1,
			wantErr:      errA,
		},
		{
			name:        "[Fail] When: the parent is cancelled during the wait ; Should: stop at once",
			policy:      fast,
			script:      []error{errA, nil},
			cancelAt:    100 * time.Millisecond,
			wantCalls:   1,
			wantElapsed: 100 * time.Millisecond,
			wantErr:     errA,
		},
		{
			name:      "[Fail] When: Attempts is 1 ; Should: call once",
			policy:    RetryPolicy{Attempts: 1, NoJitter: true},
			script:    []error{errA, nil},
			wantCalls: 1,
			wantErr:   errA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				meta := &callMeta{execution: infoExecution}

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				if tt.cancelAt > 0 {
					time.AfterFunc(tt.cancelAt, cancel)
				}

				var seen []int

				start := time.Now()
				call := scriptedCall(meta, tt.script, cancel, tt.cancelOnCall, &seen)
				err := withRetry(tt.policy, meta).Wrap(ctx, CallInfo{}, call)

				assertRetryErr(t, err, tt.wantErr)
				assertPrevious(t, meta.history(), tt.wantPrevious)
				assert.Len(t, seen, tt.wantCalls)
				assert.Equal(t, tt.wantElapsed, time.Since(start))

				for i, attempt := range seen {
					assert.Equal(t, i+1, attempt)
				}
			})
		})
	}
}

func Test_runAttempts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		draw        func(int64) int64
		cause       func(*graceBound) error
		wantSeen    []int
		wantErr     error
		wantElapsed time.Duration
	}{
		{
			name:        "[Success] When: the draw is half the bound ; Should: wait half of each delay",
			draw:        func(n int64) int64 { return n / 2 },
			wantSeen:    []int{1, 2, 3},
			wantElapsed: 300 * time.Millisecond,
		},
		{
			name:        "[Success] When: the draw is zero ; Should: retry at once",
			draw:        func(int64) int64 { return 0 },
			wantSeen:    []int{1, 2, 3},
			wantElapsed: 0,
		},
		{
			name:        "[Success] When: the draw is the top of the range ; Should: wait just under each delay",
			draw:        func(n int64) int64 { return n - 1 },
			wantSeen:    []int{1, 2, 3},
			wantElapsed: 600*time.Millisecond - 2,
		},
		{
			name: "[Fail] When: an attempt ends with the grace end of this run as its cause and the context lives ; " +
				"Should: stop without a retry",
			draw:     func(int64) int64 { return 0 },
			cause:    func(b *graceBound) error { return b.cause },
			wantSeen: []int{1},
			wantErr:  errA,
		},
		{
			name: "[Success] When: an attempt ends with ErrGraceEnded that did not come from this run ; " +
				"Should: retry as usual",
			draw:     func(int64) int64 { return 0 },
			cause:    func(*graceBound) error { return ErrGraceEnded },
			wantSeen: []int{1, 2, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				bound := newGraceBound(time.Minute)
				meta := &callMeta{execution: infoExecution, bound: bound}

				var seen []int

				script := scriptedCall(meta, []error{errA, errB, nil}, nil, 0, &seen)
				call := func(ctx context.Context) error {
					if tt.cause != nil {
						meta.end(tt.cause(bound))
					}

					return script(ctx)
				}

				start := time.Now()
				err := runAttempts(t.Context(), RetryPolicy{}, meta, call, tt.draw)

				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, tt.wantErr == nil, err == nil)
				assert.Equal(t, tt.wantSeen, seen)
				assert.Equal(t, tt.wantElapsed, time.Since(start))
			})
		})
	}
}

func Test_NoRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		change       func(*RetryPolicy)
		wantPolicy   RetryPolicy
		wantAttempts int
	}{
		{
			name:         "[Success] When: NoRetry is called ; Should: return one attempt and defaults for the rest",
			change:       func(*RetryPolicy) {},
			wantPolicy:   RetryPolicy{Attempts: 1},
			wantAttempts: 1,
		},
		{
			name:         "[Success] When: a caller changes a returned policy ; Should: leave the next call unchanged",
			change:       func(p *RetryPolicy) { p.Attempts = 9 },
			wantPolicy:   RetryPolicy{Attempts: 1},
			wantAttempts: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			first := NoRetry()
			tt.change(&first)

			got := NoRetry()
			assert.Equal(t, tt.wantPolicy, got)
			assert.Equal(t, tt.wantAttempts, got.attempts())
		})
	}
}

func Test_RetryPolicy_attempts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		attempts int
		want     int
	}{
		{
			name: "[Success] When: Attempts is zero ; Should: return DefaultAttempts",
			want: DefaultAttempts,
		},
		{
			name:     "[Success] When: Attempts is 5 ; Should: return 5",
			attempts: 5,
			want:     5,
		},
		{
			name:     "[Success] When: Attempts is UnlimitedAttempts ; Should: return the largest int",
			attempts: UnlimitedAttempts,
			want:     math.MaxInt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, RetryPolicy{Attempts: tt.attempts}.attempts())
		})
	}
}

func Test_RecoveryPolicy_retry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		policy       RecoveryPolicy
		want         RetryPolicy
		wantAttempts int
		wantInitial  time.Duration
		wantMax      time.Duration
	}{
		{
			name:         "[Success] When: the policy is zero ; Should: give unlimited attempts and the default delays",
			want:         RetryPolicy{Attempts: UnlimitedAttempts},
			wantAttempts: math.MaxInt,
			wantInitial:  DefaultInitialDelay,
			wantMax:      DefaultMaxDelay,
		},
		{
			name:         "[Success] When: Attempts is UnlimitedAttempts ; Should: give unlimited attempts",
			policy:       RecoveryPolicy{Attempts: UnlimitedAttempts},
			want:         RetryPolicy{Attempts: UnlimitedAttempts},
			wantAttempts: math.MaxInt,
			wantInitial:  DefaultInitialDelay,
			wantMax:      DefaultMaxDelay,
		},
		{
			name:         "[Success] When: Attempts is 2 ; Should: give 2 attempts",
			policy:       RecoveryPolicy{Attempts: 2},
			want:         RetryPolicy{Attempts: 2},
			wantAttempts: 2,
			wantInitial:  DefaultInitialDelay,
			wantMax:      DefaultMaxDelay,
		},
		{
			name: "[Success] When: every delay field is set ; Should: carry each one and drop AttemptTimeout",
			policy: RecoveryPolicy{
				Initial: time.Second, Max: time.Minute, Multiplier: 1.5, NoJitter: true, AttemptTimeout: time.Hour,
			},
			want: RetryPolicy{
				Attempts: UnlimitedAttempts, Initial: time.Second, Max: time.Minute, Multiplier: 1.5, NoJitter: true,
			},
			wantAttempts: math.MaxInt,
			wantInitial:  time.Second,
			wantMax:      time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.policy.retry()
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantAttempts, got.attempts())
			assert.Equal(t, tt.wantInitial, got.initial())
			assert.Equal(t, tt.wantMax, got.maxDelay())
		})
	}
}
