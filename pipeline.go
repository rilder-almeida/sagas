package sagas

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"time"
)

// ErrCallNotRun is the error of a call that middleware returned from without running the action or compensation.
var ErrCallNotRun = errors.New("sagas: middleware returned without running the call")

// ErrCallUnfinished is the error of a call that middleware returned from while the call was still running.
var ErrCallUnfinished = errors.New("sagas: middleware returned before the call finished")

// ErrCallConcurrent is the error of a call that middleware started while another call of the attempt was running.
var ErrCallConcurrent = errors.New("sagas: middleware called next while a call was running")

// ErrCallContextLost is the error of a call whose context no longer carries the call state.
var ErrCallContextLost = errors.New("sagas: middleware passed on a context that lost the call state")

// PanicError is the error a panic in a call becomes. It holds the panic value and the stack.
type PanicError struct {
	Value any
	Stack []byte
}

// Error returns the panic value in a message.
func (e *PanicError) Error() string {
	return "sagas: panic: " + fmt.Sprint(e.Value)
}

type outcomeKind string

const (
	outcomeSuccess outcomeKind = "success"
	outcomeFailure outcomeKind = "failure"
	outcomeUnknown outcomeKind = "unknown"
)

type outcome[S any] struct {
	kind     outcomeKind
	attempts int
	err      error
	state    *S
}

type callFunc[S any] func(ctx context.Context, execution string, base snapshot[S]) outcome[S]

type callSpec struct {
	saga       string
	step       string
	phase      Phase
	retry      RetryPolicy
	timeout    time.Duration
	middleware []Middleware
}

type callState[S any] struct {
	base snapshot[S]
	out  *S
}

type callStateKey struct{}

type callResult[S any] struct {
	out *S
	err error
}

type attemptProgress[S any] struct {
	started bool
	running bool
	last    callResult[S]
}

type attemptState[S any] struct {
	base     snapshot[S]
	mu       sync.Mutex
	closed   bool
	progress attemptProgress[S]
}

type attemptStateKey struct{}

func (a *attemptState[S]) start() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch {
	case a.closed:
		return ErrCallNotRun
	case a.progress.running:
		return ErrCallConcurrent
	}

	a.progress.started = true
	a.progress.running = true

	return nil
}

func (a *attemptState[S]) finish(out *S, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.progress.running = false
	a.progress.last = callResult[S]{out: out, err: err}
}

func (a *attemptState[S]) close() attemptProgress[S] {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.closed = true

	return a.progress
}

func classify(err error) outcomeKind {
	var panicked *PanicError

	switch {
	case err == nil:
		return outcomeSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrCallUnfinished),
		errors.As(err, &panicked):
		return outcomeUnknown
	default:
		return outcomeFailure
	}
}

func catchPanic(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &PanicError{Value: r, Stack: debug.Stack()}
		}
	}()

	return fn()
}

func recoverer(next Handler) Handler {
	return func(ctx context.Context) error {
		return catchPanic(func() error { return next(ctx) })
	}
}

func timeoutMiddleware(d time.Duration) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()

			return next(ctx)
		}
	}
}

func withCallState[S any](ctx context.Context, st *callState[S]) context.Context {
	return context.WithValue(ctx, callStateKey{}, st)
}

func stateFrom[S any](ctx context.Context) (*callState[S], bool) {
	st, ok := ctx.Value(callStateKey{}).(*callState[S])

	return st, ok
}

func attemptFrom[S any](ctx context.Context) (*attemptState[S], bool) {
	at, ok := ctx.Value(attemptStateKey{}).(*attemptState[S])

	return at, ok
}

func requireRan[S any](next Handler) Handler {
	return func(ctx context.Context) error {
		st, ok := stateFrom[S](ctx)
		if !ok {
			return Permanent(ErrCallContextLost)
		}

		at := &attemptState[S]{base: st.base}

		defer at.close()

		err := next(context.WithValue(ctx, attemptStateKey{}, at))
		progress := at.close()

		if progress.running {
			return Permanent(errors.Join(err, ErrCallUnfinished))
		}

		if err != nil {
			return err
		}

		if !progress.started {
			return Permanent(ErrCallNotRun)
		}

		st.out = progress.last.out

		return progress.last.err
	}
}

func runCall[S any](call func(context.Context, *S) error) Handler {
	return func(ctx context.Context) error {
		at, ok := attemptFrom[S](ctx)
		if !ok {
			return Permanent(ErrCallContextLost)
		}

		if err := at.start(); err != nil {
			return Permanent(err)
		}

		fresh, err := copyState(at.base)
		if err != nil {
			err = Permanent(err)
		} else if err = catchPanic(func() error { return call(ctx, fresh) }); err != nil {
			fresh = nil
		}

		at.finish(fresh, err)

		return err
	}
}

func pipeline[S any](spec *callSpec, call func(context.Context, *S) error) callFunc[S] {
	layers := []Middleware{retryMiddleware(spec.retry), withStepInfo(spec.saga, spec.step, spec.phase)}
	if spec.phase == PhaseAction && spec.timeout > 0 {
		layers = append(layers, timeoutMiddleware(spec.timeout))
	}

	layers = append(layers, recoverer, requireRan[S])
	layers = append(layers, spec.middleware...)

	chain := runCall(call)
	for _, layer := range slices.Backward(layers) {
		chain = layer(chain)
	}

	return func(ctx context.Context, execution string, base snapshot[S]) outcome[S] {
		meta := &callMeta{execution: execution}
		st := &callState[S]{base: base}

		err := chain(withCallState(withMeta(ctx, meta), st))

		out := outcome[S]{kind: classify(err), attempts: meta.attempt, err: err}
		if err == nil {
			out.state = st.out
		}

		return out
	}
}
