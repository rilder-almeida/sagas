package sagas

import (
	"context"
	"errors"
	"fmt"
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
		name        string
		in          error
		wantNil     bool
		wantMessage string
	}{
		{
			name:        "[Success] When: the error is plain ; Should: mark it and keep it in the chain",
			in:          errBoom,
			wantMessage: errBoom.Error(),
		},
		{
			name:    "[Success] When: the error is nil ; Should: return nil",
			in:      nil,
			wantNil: true,
		},
		{
			name:        "[Success] When: the error is already permanent ; Should: stay permanent, same message",
			in:          Permanent(errBoom),
			wantMessage: errBoom.Error(),
		},
		{
			name:        "[Success] When: a permanent error is wrapped ; Should: still detect it",
			in:          fmt.Errorf("x: %w", Permanent(errBoom)),
			wantMessage: "x: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Permanent(tt.in)
			if tt.wantNil {
				assert.NoError(t, got)

				return
			}

			require.ErrorIs(t, got, tt.in)
			assert.True(t, isPermanent(got))
			assert.Equal(t, tt.wantMessage, got.Error())
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
			name:    "[Success] When: jitter draws the top of the range ; Should: wait the full delay",
			policy:  RetryPolicy{},
			attempt: 2,
			r:       upper,
			want:    400 * time.Millisecond,
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

func scriptedHandler(script []error, cancel context.CancelFunc, cancelOnCall int, seen *[]int) Handler {
	calls := 0

	return func(ctx context.Context) error {
		calls++

		*seen = append(*seen, metaFrom(ctx).attempt)

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

func Test_retry(t *testing.T) {
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
	}{
		{
			name:      "[Success] When: the first call succeeds ; Should: call once",
			policy:    fast,
			script:    []error{nil},
			wantCalls: 1,
		},
		{
			name:        "[Success] When: the third call succeeds ; Should: retry twice with growing waits",
			policy:      fast,
			script:      []error{errA, errB, nil},
			wantCalls:   3,
			wantElapsed: 600 * time.Millisecond,
		},
		{
			name:        "[Fail] When: every attempt fails ; Should: stop at the limit with the last error",
			policy:      fast,
			script:      []error{errA, errB, errC},
			wantCalls:   3,
			wantElapsed: 600 * time.Millisecond,
			wantErr:     errC,
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
			script:      []error{errA, errA, errA, nil},
			wantCalls:   4,
			wantElapsed: 9 * time.Second,
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
			name:        "[Success] When: an attempt hits its own deadline ; Should: retry it",
			policy:      fast,
			script:      []error{errAttemptLimit, nil},
			wantCalls:   2,
			wantElapsed: 1200 * time.Millisecond,
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
				ctx, cancel := context.WithCancel(withMeta(t.Context(), &callMeta{execution: infoExecution}))
				defer cancel()

				if tt.cancelAt > 0 {
					time.AfterFunc(tt.cancelAt, cancel)
				}

				var seen []int

				start := time.Now()
				err := retry(tt.policy)(scriptedHandler(tt.script, cancel, tt.cancelOnCall, &seen))(ctx)

				if tt.wantErr == nil {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, tt.wantErr)
				}

				assert.Len(t, seen, tt.wantCalls)
				assert.Equal(t, tt.wantElapsed, time.Since(start))

				for i, attempt := range seen {
					assert.Equal(t, i+1, attempt)
				}
			})
		})
	}
}
