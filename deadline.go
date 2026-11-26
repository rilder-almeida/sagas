package sagas

import (
	"context"
	"errors"
	"sync"
	"time"
)

type graceBound struct {
	grace  time.Duration
	caller <-chan struct{}
	limit  time.Time
	mu     sync.Mutex
	ended  time.Time
}

func (b *graceBound) deadline() (time.Time, bool) {
	if b == nil {
		return time.Time{}, false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.ended.IsZero() {
		select {
		case <-b.caller:
			b.ended = time.Now()
			if !b.limit.IsZero() && b.limit.Before(b.ended) {
				b.ended = b.limit
			}
		default:
			return time.Time{}, false
		}
	}

	return b.ended.Add(b.grace), true
}

func (b *graceBound) owns(cause error) bool {
	end, ended := b.deadline()

	return ended && errors.Is(cause, ErrGraceEnded) && !time.Now().Before(end)
}

func (b *graceBound) cut(grace context.Context, out *outcome) bool {
	if b.owns(out.cause) {
		<-grace.Done()
	}

	return graceEnded(grace)
}

func (m *callMeta) graceCut() bool {
	return m.bound.owns(m.cause)
}

func withDeadline(meta *callMeta, d time.Duration, cause error) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		parent, at, why := ctx, time.Now().Add(d), cause
		if end, ended := meta.bound.deadline(); ended && !at.Before(end) {
			at, why = end, ErrGraceEnded
			if inherited, ok := ctx.Deadline(); !ok || inherited.After(end) {
				parent = context.WithoutCancel(ctx)
			}
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
	grace, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	expire := context.CancelFunc(func() {})

	callerDeadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		grace, expire = context.WithDeadlineCause(grace, callerDeadline.Add(d), ErrGraceEnded)
	}

	bound := &graceBound{grace: d, caller: ctx.Done(), limit: callerDeadline}
	done := make(chan struct{})

	go func() {
		defer close(done)

		select {
		case <-ctx.Done():
		case <-grace.Done():
			return
		}

		end, _ := bound.deadline()
		if hasDeadline && !end.Before(callerDeadline.Add(d)) {
			return
		}

		timer := time.NewTimer(time.Until(end))
		defer timer.Stop()

		select {
		case <-timer.C:
			cancel(ErrGraceEnded)
		case <-grace.Done():
		}
	}()

	return grace, bound, func() {
		cancel(nil)
		expire()
		<-done
	}
}
