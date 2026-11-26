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

func Test_graceContext_gatedCaller(t *testing.T) {
	t.Parallel()

	const grace = time.Minute

	tests := []struct {
		name      string
		deadline  time.Duration
		closeAt   time.Duration
		callerErr error
		wantEnd   time.Duration
	}{
		{
			name: "[Success] When: the caller never ends and the helper goroutine is held ; " +
				"Should: report no bound and return from release only after the goroutine exits",
		},
		{
			name: "[Success] When: the caller has no deadline and ends before the first read ; " +
				"Should: record the read instant",
			closeAt:   time.Hour,
			callerErr: context.Canceled,
			wantEnd:   2*time.Hour + grace,
		},
		{
			name: "[Success] When: the caller ends at its deadline D and the first read comes after D ; " +
				"Should: record D",
			deadline:  time.Hour,
			closeAt:   time.Hour,
			callerErr: context.DeadlineExceeded,
			wantEnd:   time.Hour + grace,
		},
		{
			name: "[Success] When: the caller cancels before its deadline D and the first read comes after D ; " +
				"Should: record D",
			deadline:  time.Hour,
			closeAt:   time.Second,
			callerErr: context.Canceled,
			wantEnd:   time.Hour + grace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				caller := &gatedContext{gate: make(chan struct{}), free: 1, caller: make(chan struct{})}

				if tt.deadline > 0 {
					caller.deadline = start.Add(tt.deadline)
				}

				if tt.closeAt > 0 {
					time.AfterFunc(tt.closeAt, func() {
						caller.err = tt.callerErr
						close(caller.caller)
					})
				}

				_, bound, release := graceContext(caller, grace)

				time.Sleep(2 * time.Hour)

				assert.Equal(t, tt.wantEnd, boundAfter(bound, start))
				assert.Equal(t, tt.callerErr, caller.Err())

				returned := make(chan struct{})

				go func() {
					release()
					close(returned)
				}()

				synctest.Wait()
				assert.False(t, isClosed(returned), "release returned while the helper goroutine was alive")

				close(caller.gate)
				synctest.Wait()
				assert.True(t, isClosed(returned), "release did not return after the helper goroutine exited")
			})
		})
	}
}

type gatedContext struct {
	gate     chan struct{}
	free     int32
	calls    atomic.Int32
	caller   chan struct{}
	err      error
	deadline time.Time
}

func (c *gatedContext) Deadline() (time.Time, bool) { return c.deadline, !c.deadline.IsZero() }

func (c *gatedContext) Done() <-chan struct{} {
	if c.calls.Add(1) > c.free {
		<-c.gate
	}

	return c.caller
}

func (c *gatedContext) Err() error {
	select {
	case <-c.caller:
		return c.err
	default:
		return nil
	}
}

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
		name    string
		limit   time.Duration
		closeAt time.Duration
		reads   []time.Duration
		wantEnd time.Duration
	}{
		{
			name:  "[Success] When: the caller channel never closes ; Should: report no bound",
			reads: []time.Duration{time.Hour},
		},
		{
			name: "[Success] When: the bound is read at the instant the caller channel closes ; " +
				"Should: record that instant",
			closeAt: time.Second,
			reads:   []time.Duration{time.Second},
			wantEnd: time.Second + grace,
		},
		{
			name:    "[Success] When: the bound is read again later ; Should: keep the instant of the first read",
			closeAt: time.Second,
			reads:   []time.Duration{time.Second, time.Hour},
			wantEnd: time.Second + grace,
		},
		{
			name: "[Success] When: the first read comes after the caller channel closed ; " +
				"Should: record the read instant",
			closeAt: time.Second,
			reads:   []time.Duration{time.Hour},
			wantEnd: time.Hour + grace,
		},
		{
			name:    "[Success] When: the first read comes before the caller limit ; Should: record the read instant",
			limit:   time.Hour,
			closeAt: time.Second,
			reads:   []time.Duration{time.Second},
			wantEnd: time.Second + grace,
		},
		{
			name: "[Success] When: the caller channel closed at its limit and the first read comes later ; " +
				"Should: record the limit",
			limit:   time.Hour,
			closeAt: time.Hour,
			reads:   []time.Duration{2 * time.Hour},
			wantEnd: time.Hour + grace,
		},
		{
			name: "[Success] When: the caller channel closed before its limit and the first read comes after it ; " +
				"Should: record the limit",
			limit:   time.Hour,
			closeAt: time.Second,
			reads:   []time.Duration{2 * time.Hour},
			wantEnd: time.Hour + grace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				caller := make(chan struct{})
				bound := &graceBound{grace: grace, caller: caller}

				if tt.limit > 0 {
					bound.limit = start.Add(tt.limit)
				}

				if tt.closeAt > 0 {
					time.AfterFunc(tt.closeAt, func() { close(caller) })
				}

				var got time.Duration

				for _, at := range tt.reads {
					time.Sleep(at - time.Since(start))
					synctest.Wait()

					got = boundAfter(bound, start)
				}

				assert.Equal(t, tt.wantEnd, got)
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
