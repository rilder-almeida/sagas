package sagas

import (
	"context"
	"errors"
	"sync"
	"time"
)

type graceEndError struct {
	grace time.Duration
}

func (*graceEndError) Error() string { return ErrGraceEnded.Error() }

func (*graceEndError) Is(target error) bool { return target == ErrGraceEnded }

type graceBound struct {
	grace time.Duration
	cause error
	mu    sync.Mutex
	ended time.Time
}

func newGraceBound(d time.Duration) *graceBound {
	return &graceBound{grace: d, cause: &graceEndError{grace: d}}
}

func (b *graceBound) end(at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ended = at
}

func (b *graceBound) deadline() (time.Time, bool) {
	if b == nil {
		return time.Time{}, false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.ended.IsZero() {
		return time.Time{}, false
	}

	return b.ended.Add(b.grace), true
}

func (b *graceBound) cut(grace context.Context, out *outcome) bool {
	if errors.Is(out.cause, b.cause) {
		<-grace.Done()
	}

	return graceEnded(grace)
}

func (m *callMeta) graceCut() bool {
	return m.bound != nil && errors.Is(m.cause, m.bound.cause)
}

func withDeadline(meta *callMeta, d time.Duration, cause error) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		parent, at, why := ctx, time.Now().Add(d), cause
		if end, ended := meta.bound.deadline(); ended && !at.Before(end) {
			parent, at, why = context.WithoutCancel(ctx), end, meta.bound.cause
		}

		var cancel context.CancelFunc
		if inherited, ok := parent.Deadline(); ok && !inherited.After(at) {
			ctx, cancel = context.WithCancel(parent)
		} else {
			ctx, cancel = context.WithDeadlineCause(parent, at, why)
		}
		defer cancel()

		err := call(ctx)
		if err != nil && ctx.Err() != nil {
			meta.end(context.Cause(ctx))
		}

		return err
	}
}

type waitRelease func()

func graceContext(ctx context.Context, d time.Duration) (context.Context, *graceBound, waitRelease) {
	bound := newGraceBound(d)
	grace, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	expire := context.CancelFunc(func() {})

	callerDeadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		grace, expire = context.WithDeadlineCause(grace, callerDeadline.Add(d), bound.cause)
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
			return
		}

		timer := time.NewTimer(d)
		defer timer.Stop()

		bound.end(time.Now())

		select {
		case <-timer.C:
			cancel(bound.cause)
		case <-grace.Done():
		}
	}()

	return grace, bound, func() {
		cancel(nil)
		expire()
		<-done
	}
}
