package sagas

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
)

func callerContext(t *testing.T, start time.Time, deadline time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()

	ctx := context.WithValue(t.Context(), graceKey{}, "kept")
	if deadline == 0 {
		return context.WithCancel(ctx)
	}

	return context.WithDeadline(ctx, start.Add(deadline))
}

func endedBound(t *testing.T, d time.Duration) *graceBound {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, bound, release := graceContext(ctx, d)
	t.Cleanup(release)

	return bound
}

func deadlineAfter(ctx context.Context, start time.Time) time.Duration {
	if at, ok := ctx.Deadline(); ok {
		return at.Sub(start)
	}

	return 0
}

func Test_graceContext(t *testing.T) {
	t.Parallel()

	const grace = time.Minute

	tests := []struct {
		name         string
		releaseFirst bool
		cancelCaller bool
		deadline     time.Duration
		wait         time.Duration
		wantOpen     bool
		wantCause    error
		wantDeadline time.Duration
	}{
		{
			name:     "[Success] When: the caller never cancels ; Should: keep the grace context open with no timer",
			wait:     24 * time.Hour,
			wantOpen: true,
		},
		{
			name:         "[Success] When: the caller cancels ; Should: keep the grace context open until d elapses",
			cancelCaller: true,
			wait:         grace - time.Nanosecond,
			wantOpen:     true,
		},
		{
			name:         "[Fail] When: the caller cancels and d elapses ; Should: end the grace context",
			cancelCaller: true,
			wait:         grace,
			wantOpen:     false,
			wantCause:    ErrGraceEnded,
		},
		{
			name:         "[Fail] When: release runs before the caller cancels ; Should: end the grace context",
			releaseFirst: true,
			cancelCaller: true,
			wantOpen:     false,
			wantCause:    context.Canceled,
		},
		{
			name:         "[Success] When: the caller has deadline D ; Should: report D plus the grace period",
			deadline:     time.Hour,
			wait:         time.Hour + grace - time.Nanosecond,
			wantOpen:     true,
			wantDeadline: time.Hour + grace,
		},
		{
			name: "[Fail] When: the caller has deadline D and D passes ; " +
				"Should: end the grace context at D plus the grace period with ErrGraceEnded",
			deadline:     time.Hour,
			wait:         time.Hour + grace,
			wantOpen:     false,
			wantCause:    ErrGraceEnded,
			wantDeadline: time.Hour + grace,
		},
		{
			name: "[Fail] When: the caller has deadline D and cancels at t before D ; " +
				"Should: end the grace context at t plus the grace period",
			cancelCaller: true,
			deadline:     time.Hour,
			wait:         grace,
			wantOpen:     false,
			wantCause:    ErrGraceEnded,
			wantDeadline: time.Hour + grace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				start := time.Now()

				ctx, cancel := callerContext(t, start, tt.deadline)
				defer cancel()

				g, _, release := graceContext(ctx, grace)
				defer release()

				if tt.releaseFirst {
					release()
				}

				if tt.cancelCaller {
					cancel()
				}

				time.Sleep(tt.wait)
				synctest.Wait()

				assert.Equal(t, tt.wantOpen, g.Err() == nil)
				assert.Equal(t, tt.wantCause, context.Cause(g))
				assert.Equal(t, "kept", g.Value(graceKey{}))

				assert.Equal(t, tt.wantDeadline, deadlineAfter(g, start))
			})
		})
	}
}

func Test_graceContext_releaseWaits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "[Success] When: the helper goroutine is still running ; Should: return only after it exits"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				gate := make(chan struct{})
				_, _, release := graceContext(&gatedContext{gate: gate}, time.Minute)

				returned := make(chan struct{})

				go func() {
					release()
					close(returned)
				}()

				synctest.Wait()
				assert.False(t, isClosed(returned), "release returned while the helper goroutine was alive")

				close(gate)
				synctest.Wait()
				assert.True(t, isClosed(returned), "release did not return after the helper goroutine exited")
			})
		})
	}
}

type gatedContext struct {
	gate  chan struct{}
	calls atomic.Int32
}

func (*gatedContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *gatedContext) Done() <-chan struct{} {
	if c.calls.Add(1) > 1 {
		<-c.gate
	}

	return nil
}

func (*gatedContext) Err() error { return nil }

func (*gatedContext) Value(any) any { return nil }

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func boundAfter(b *graceBound, start time.Time) time.Duration {
	if end, ok := b.deadline(); ok {
		return end.Sub(start)
	}

	return 0
}

func Test_graceBound_deadline(t *testing.T) {
	t.Parallel()

	const grace = time.Minute

	tests := []struct {
		name     string
		deadline time.Duration
		cancelAt time.Duration
		readAt   time.Duration
		wantEnd  time.Duration
	}{
		{
			name:   "[Success] When: the caller did not end ; Should: report no bound",
			readAt: time.Hour,
		},
		{
			name: "[Success] When: the bound is read at the instant the caller cancels ; " +
				"Should: report t plus the grace period",
			cancelAt: time.Second,
			readAt:   time.Second,
			wantEnd:  time.Second + grace,
		},
		{
			name:     "[Success] When: the bound is read after the caller cancelled at t ; Should: keep t",
			cancelAt: time.Second,
			readAt:   time.Hour,
			wantEnd:  time.Second + grace,
		},
		{
			name:     "[Success] When: the caller deadline D passed ; Should: report D plus the grace period",
			deadline: time.Hour,
			readAt:   2 * time.Hour,
			wantEnd:  time.Hour + grace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				start := time.Now()

				ctx, cancel := callerContext(t, start, tt.deadline)
				defer cancel()

				_, bound, release := graceContext(ctx, grace)
				defer release()

				if tt.cancelAt > 0 {
					time.Sleep(tt.cancelAt)
					cancel()
				}

				time.Sleep(tt.readAt - tt.cancelAt)

				assert.Equal(t, tt.wantEnd, boundAfter(bound, start))
			})
		})
	}
}

func Test_graceBound_owns(t *testing.T) {
	t.Parallel()

	const grace = time.Minute

	tests := []struct {
		name   string
		cancel bool
		wait   time.Duration
		cause  error
		want   bool
	}{
		{
			name:  "[Fail] When: the caller did not end ; Should: not own ErrGraceEnded",
			wait:  time.Hour,
			cause: ErrGraceEnded,
		},
		{
			name:   "[Fail] When: ErrGraceEnded arrives before the bound ; Should: not own it",
			cancel: true,
			wait:   grace - time.Nanosecond,
			cause:  ErrGraceEnded,
		},
		{
			name:   "[Success] When: ErrGraceEnded arrives at the bound ; Should: own it",
			cancel: true,
			wait:   grace,
			cause:  ErrGraceEnded,
			want:   true,
		},
		{
			name:   "[Success] When: a wrapped ErrGraceEnded arrives after the bound ; Should: own it",
			cancel: true,
			wait:   time.Hour,
			cause:  fmt.Errorf("outer: %w", ErrGraceEnded),
			want:   true,
		},
		{
			name:   "[Fail] When: another cause arrives after the bound ; Should: not own it",
			cancel: true,
			wait:   time.Hour,
			cause:  context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				_, bound, release := graceContext(ctx, grace)
				defer release()

				if tt.cancel {
					cancel()
				}

				time.Sleep(tt.wait)

				assert.Equal(t, tt.want, bound.owns(tt.cause))
			})
		})
	}
}
