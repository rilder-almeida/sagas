package sagas

import (
	"context"
	"errors"
	"sync"
	"time"
)

type graceBoundKey struct{}

type graceBound struct {
	grace time.Duration
	mu    sync.Mutex
	ended time.Time
}

func (b *graceBound) end(at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ended = at
}

func (b *graceBound) deadline() (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.ended.IsZero() {
		return time.Time{}, false
	}

	return b.ended.Add(b.grace), true
}

func withDeadline(meta *callMeta, d time.Duration, cause error) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		parent, at, why := ctx, time.Now().Add(d), cause
		if b, ok := ctx.Value(graceBoundKey{}).(*graceBound); ok {
			if end, ended := b.deadline(); ended && !at.Before(end) {
				parent, at, why = context.WithoutCancel(ctx), end, ErrGraceEnded
			}
		}

		ctx, cancel := context.WithDeadlineCause(parent, at, why)
		defer cancel()

		err := call(ctx)
		if err != nil && ctx.Err() != nil {
			meta.end(context.Cause(ctx))
		}

		return err
	}
}

type waitRelease func()

func graceContext(ctx context.Context, d time.Duration) (context.Context, waitRelease) {
	bound := &graceBound{grace: d}
	grace, cancel := context.WithCancelCause(context.WithValue(context.WithoutCancel(ctx), graceBoundKey{}, bound))
	expire := context.CancelFunc(func() {})

	callerDeadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		grace, expire = context.WithDeadlineCause(grace, callerDeadline.Add(d), ErrGraceEnded)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		select {
		case <-ctx.Done():
		case <-grace.Done():
			return
		}

		if hasDeadline && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			bound.end(callerDeadline)
			<-grace.Done()

			return
		}

		timer := time.NewTimer(d)
		defer timer.Stop()

		bound.end(time.Now())

		select {
		case <-timer.C:
			cancel(ErrGraceEnded)
		case <-grace.Done():
		}
	}()

	return grace, func() {
		cancel(nil)
		expire()
		<-done
	}
}
