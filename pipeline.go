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
var ErrCallNotRun = &KindError{name: "call_not_run", message: "sagas: middleware returned without running the call"}

// ErrCallUnfinished is the error of a call that middleware returned from while the call was still running.
var ErrCallUnfinished = &KindError{
	name: "call_unfinished", message: "sagas: middleware returned before the call finished",
}

// ErrCallConcurrent is the error of a call that middleware started while another call of the attempt was running.
var ErrCallConcurrent = &KindError{
	name: "call_concurrent", message: "sagas: middleware called next while a call was running",
}

// ErrCallContextLost is the error of a call whose context no longer carries the call state.
var ErrCallContextLost = &KindError{
	name: "call_context_lost", message: "sagas: middleware passed on a context that lost the call state",
}

// PanicError is the error a panic in a call becomes. It holds the panic value and the stack.
type PanicError struct {
	Value any
	Stack []byte
}

// Error returns the panic value in a message.
func (e *PanicError) Error() string {
	return "sagas: panic: " + fmt.Sprint(e.Value)
}

// Unwrap returns the panic value when it is an error.
func (e *PanicError) Unwrap() error {
	if err, ok := e.Value.(error); ok {
		return err
	}

	return nil
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
	label    *KindError
	cause    error
	previous []error
	state    *S
}

type callFunc[S any] func(ctx context.Context, meta *callMeta, base snapshot[S]) outcome[S]

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
	label   *KindError
	last    callResult[S]
}

type attemptState[S any] struct {
	base     snapshot[S]
	mu       sync.Mutex
	closed   bool
	progress attemptProgress[S]
}

type attemptStateKey struct{}

func (a *attemptState[S]) start() *KindError {
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

func (a *attemptState[S]) finish(out *S, err error, label *KindError) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.progress.running = false
	a.progress.label = label
	a.progress.last = callResult[S]{out: out, err: err}
}

func (a *attemptState[S]) refuse(label *KindError) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.progress.label = label
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

func recoverPanic(fn func() error) (bool, error) {
	var caught *PanicError

	err := func() error {
		defer func() {
			if r := recover(); r != nil {
				caught = &PanicError{Value: r, Stack: debug.Stack()}
			}
		}()

		return fn()
	}()

	if caught != nil {
		return true, caught
	}

	return false, err
}

func recoverer(next Handler) Handler {
	return func(ctx context.Context) error {
		panicked, err := recoverPanic(func() error { return next(ctx) })
		if panicked {
			metaFrom(ctx).mark(ErrPanic)
		}

		return err
	}
}

func withTimeout(step string, d time.Duration) Middleware {
	cause := fmt.Errorf("sagas: step %q timed out after %v: %w", step, d, context.DeadlineExceeded)

	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			ctx, cancel := context.WithTimeoutCause(ctx, d, cause)
			defer cancel()

			err := next(ctx)
			if err != nil && ctx.Err() != nil {
				metaFrom(ctx).end(context.Cause(ctx))
			}

			return err
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

type contextLostError struct{}

func (contextLostError) Error() string { return ErrCallContextLost.Error() }

func (contextLostError) Unwrap() error { return ErrCallContextLost }

func requireRan[S any](next Handler) Handler {
	return func(ctx context.Context) error {
		meta := metaFrom(ctx)

		st, ok := stateFrom[S](ctx)
		if !ok {
			meta.mark(ErrCallContextLost)

			return Permanent(ErrCallContextLost)
		}

		at := &attemptState[S]{base: st.base}

		defer at.close()

		err := next(context.WithValue(ctx, attemptStateKey{}, at))
		progress := at.close()

		if progress.running {
			meta.mark(ErrCallUnfinished)

			return Permanent(errors.Join(err, ErrCallUnfinished))
		}

		if err != nil {
			noteCallError(meta, err, &progress)

			return err
		}

		if !progress.started {
			meta.mark(ErrCallNotRun)

			return Permanent(ErrCallNotRun)
		}

		meta.mark(progress.label)

		st.out = progress.last.out

		return progress.last.err
	}
}

func noteCallError[S any](meta *callMeta, err error, progress *attemptProgress[S]) {
	switch {
	case progress.last.err != nil && !errors.Is(err, progress.last.err):
		meta.replace(progress.last.err)
	case errors.Is(err, contextLostError{}):
		meta.mark(ErrCallContextLost)
	default:
		meta.mark(progress.label)
	}
}

func runCall[S any](call func(context.Context, *S) error) Handler {
	return func(ctx context.Context) error {
		at, ok := attemptFrom[S](ctx)
		if !ok {
			return Permanent(contextLostError{})
		}

		if kind := at.start(); kind != nil {
			at.refuse(kind)

			return Permanent(kind)
		}

		var label *KindError

		fresh, err := copyState(at.base)
		if err != nil {
			label, err = ErrInvalidState, Permanent(err)
		} else if panicked, callErr := recoverPanic(func() error { return call(ctx, fresh) }); callErr != nil {
			fresh, err = nil, callErr

			if panicked {
				label = ErrPanic
			}
		}

		at.finish(fresh, err, label)

		return err
	}
}

func pipeline[S any](spec *callSpec, call func(context.Context, *S) error) callFunc[S] {
	layers := []Middleware{withRetry(spec.retry), withStepInfo(spec.saga, spec.step, spec.phase)}
	if spec.phase == PhaseAction && spec.timeout > 0 {
		layers = append(layers, withTimeout(spec.step, spec.timeout))
	}

	layers = append(layers, recoverer, requireRan[S])
	layers = append(layers, spec.middleware...)

	chain := runCall(call)
	for _, layer := range slices.Backward(layers) {
		chain = layer(chain)
	}

	return func(ctx context.Context, meta *callMeta, base snapshot[S]) outcome[S] {
		st := &callState[S]{base: base}

		err := chain(withCallState(withMeta(ctx, meta), st))

		out := outcome[S]{kind: classify(err), attempts: meta.attempt}
		if err == nil {
			out.state = st.out

			return out
		}

		out.err, out.label, out.cause, out.previous = err, meta.label, meta.cause, meta.history()

		if out.cause == nil && ctx.Err() != nil {
			out.cause = context.Cause(ctx)
		}

		return out
	}
}
