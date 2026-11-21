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
	name: "call_concurrent", message: "sagas: middleware ran the call again while it was running",
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
	wrap       Middleware
}

// MiddlewareFunc adapts a function with the signature of Wrap to Middleware.
type MiddlewareFunc func(ctx context.Context, info CallInfo, call Call) error

// Wrap calls f.
func (f MiddlewareFunc) Wrap(ctx context.Context, info CallInfo, call Call) error {
	return f(ctx, info, call)
}

// Chain returns middleware that runs mws around a call, the first one outermost. It panics if an item is nil.
func Chain(mws ...Middleware) MiddlewareFunc {
	checkChain(mws)

	return compose(slices.Clone(mws))
}

func checkChain(mws []Middleware) {
	for i, mw := range mws {
		if isNilValue(mw) {
			panic(fmt.Sprintf("sagas: Chain item %d is nil", i))
		}
	}
}

func compose(mws []Middleware) MiddlewareFunc {
	return func(ctx context.Context, info CallInfo, call Call) error {
		for _, mw := range slices.Backward(mws) {
			inner := call
			call = func(ctx context.Context) error { return mw.Wrap(ctx, info, inner) }
		}

		return call(ctx)
	}
}

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

func recoverer(meta *callMeta) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		panicked, err := recoverPanic(func() error { return call(ctx) })
		if panicked {
			meta.mark(ErrPanic)
		}

		return err
	}
}

func withTimeout(meta *callMeta, d time.Duration, cause error) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		ctx, cancel := context.WithTimeoutCause(ctx, d, cause)
		defer cancel()

		err := call(ctx)
		if err != nil && ctx.Err() != nil {
			meta.end(context.Cause(ctx))
		}

		return err
	}
}

func requireRan[S any](at *attemptState[S], meta *callMeta, out **S) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		defer at.close()

		err := call(ctx)
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

		*out = progress.last.out

		return progress.last.err
	}
}

func noteCallError[S any](meta *callMeta, err error, progress *attemptProgress[S]) {
	if progress.last.err != nil && !errors.Is(err, progress.last.err) {
		meta.replace(progress.last.err)

		return
	}

	meta.mark(progress.label)
}

func runCall[S any](at *attemptState[S], call func(context.Context, *S) error, key string) Call {
	return func(ctx context.Context) error {
		if kind := at.start(); kind != nil {
			at.refuse(kind)

			return Permanent(kind)
		}

		ctx = withIdempotencyKey(ctx, key)

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

func pipeline[S any](given *callSpec, call func(context.Context, *S) error) callFunc[S] {
	spec := new(*given)

	var timeoutCause error
	if spec.timesOut() {
		timeoutCause = fmt.Errorf("sagas: step %q timed out after %v: %w", spec.step, spec.timeout,
			context.DeadlineExceeded)
	}

	return func(ctx context.Context, meta *callMeta, base snapshot[S]) outcome[S] {
		var state *S

		info := CallInfo{Saga: spec.saga, Execution: meta.execution, Step: spec.step, Phase: spec.phase}
		key := info.IdempotencyKey()

		attempt := func(ctx context.Context) error {
			at := &attemptState[S]{base: base}

			attemptInfo := info
			attemptInfo.Attempt = meta.attempt

			layers := compose(attemptLayers(spec, meta, at, &state, timeoutCause))

			return layers.Wrap(ctx, attemptInfo, runCall(at, call, key))
		}

		err := withRetry(spec.retry, meta).Wrap(ctx, info, attempt)

		out := outcome[S]{kind: classify(err), attempts: meta.attempt}
		if err == nil {
			out.state = state

			return out
		}

		out.err, out.label, out.cause, out.previous = err, meta.label, meta.cause, meta.history()

		if out.cause == nil && ctx.Err() != nil {
			out.cause = context.Cause(ctx)
		}

		return out
	}
}

const coreLayers = 4

func (s *callSpec) timesOut() bool {
	return s.phase != PhaseCompensate && s.timeout > 0
}

func attemptLayers[S any](
	spec *callSpec, meta *callMeta, at *attemptState[S], state **S, timeoutCause error,
) []Middleware {
	layers := make([]Middleware, 0, len(spec.middleware)+coreLayers)

	if spec.timesOut() {
		layers = append(layers, withTimeout(meta, spec.timeout, timeoutCause))
	}

	layers = append(layers, recoverer(meta), requireRan(at, meta, state))
	layers = append(layers, spec.middleware...)

	if spec.wrap != nil {
		layers = append(layers, spec.wrap)
	}

	return layers
}
