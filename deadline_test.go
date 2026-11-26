package sagas

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func callerContext(t *testing.T, start time.Time, deadline time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()

	ctx := context.WithValue(t.Context(), graceKey{}, "kept")
	if deadline == 0 {
		return context.WithCancel(ctx)
	}

	return context.WithDeadline(ctx, start.Add(deadline))
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
				require.ErrorIs(t, context.Cause(g), tt.wantCause)
				assert.Equal(t, tt.wantCause == nil, context.Cause(g) == nil)
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
				_, _, release := graceContext(gatedContext{gate: gate}, time.Minute)

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
	gate chan struct{}
}

func (gatedContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c gatedContext) Done() <-chan struct{} {
	<-c.gate

	return nil
}

func (gatedContext) Err() error { return nil }

func (gatedContext) Value(any) any { return nil }

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func Test_graceBound_deadline(t *testing.T) {
	t.Parallel()

	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		ended  time.Time
		want   time.Time
		wantOK bool
	}{
		{
			name: "[Success] When: the caller did not end ; Should: report no bound",
		},
		{
			name:   "[Success] When: the caller ended at t ; Should: report t plus the grace period",
			ended:  epoch.Add(time.Second),
			want:   epoch.Add(time.Second + time.Minute),
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := &graceBound{grace: time.Minute}
			b.end(tt.ended)

			got, ok := b.deadline()
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func Test_graceEndError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		grace time.Duration
		want  string
	}{
		{
			name:  "[Success] When: the grace period is one minute ; Should: read as ErrGraceEnded",
			grace: time.Minute,
			want:  "sagas: recovery grace period ended",
		},
		{
			name: "[Success] When: the grace period is zero ; Should: read as ErrGraceEnded",
			want: "sagas: recovery grace period ended",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, (&graceEndError{grace: tt.grace}).Error())
		})
	}
}

func Test_graceEndError_Is(t *testing.T) {
	t.Parallel()

	own := newGraceBound(time.Minute).cause

	tests := []struct {
		name   string
		target error
		want   bool
	}{
		{
			name:   "[Success] When: the target is ErrGraceEnded ; Should: match it",
			target: ErrGraceEnded,
			want:   true,
		},
		{
			name:   "[Fail] When: the target is another kind ; Should: not match it",
			target: ErrFailed,
		},
		{
			name:   "[Fail] When: the target is the grace end of another run ; Should: not match it",
			target: newGraceBound(time.Minute).cause,
		},
		{
			name:   "[Success] When: the target is the same grace end ; Should: match it",
			target: own,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, errors.Is(own, tt.target))
		})
	}
}
