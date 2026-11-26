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
var ErrCallNotRun = &KindError{name: "call-not-run", message: "sagas: middleware returned without running the call"}

// ErrCallUnfinished is the error of a call that middleware returned from while the call was still running.
var ErrCallUnfinished = &KindError{
	name: "call-unfinished", message: "sagas: middleware returned before the call finished",
}

// ErrCallConcurrent is the error of a call that middleware started while another call of the attempt was running.
var ErrCallConcurrent = &KindError{
	name: "call-concurrent", message: "sagas: middleware ran the call again while it was running",
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

type outcome struct {
	kind     outcomeKind
	attempts int
	err      error
	label    *KindError
	cause    error
	previous []error
	output   encodedOutput
}

type callFunc[S any] func(ctx context.Context, meta *callMeta, base snapshot[S]) outcome

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

type callResult struct {
	out encodedOutput
	err error
}

type attemptProgress struct {
	started bool
	running bool
	label   *KindError
	last    callResult
}

type attemptState[S any] struct {
	base     snapshot[S]
	mu       sync.Mutex
	closed   bool
	progress attemptProgress
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

func (a *attemptState[S]) finish(out encodedOutput, err error, label *KindError) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.progress.running = false
	a.progress.label = label
	a.progress.last = callResult{out: out, err: err}
}

func (a *attemptState[S]) refuse(label *KindError) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.progress.label = label
}

func (a *attemptState[S]) close() attemptProgress {
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

type graceBoundKey struct{}

type graceBound struct {
	grace  time.Duration
	caller time.Time
	mu     sync.Mutex
	ended  time.Time
}

func (b *graceBound) end(at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ended = at
}

func (b *graceBound) deadline() (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch {
	case !b.ended.IsZero():
		return b.ended.Add(b.grace), true
	case !b.caller.IsZero():
		return b.caller.Add(b.grace), true
	default:
		return time.Time{}, false
	}
}

func withDeadline(meta *callMeta, d time.Duration, cause error) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		at, why := time.Now().Add(d), cause
		if b, ok := ctx.Value(graceBoundKey{}).(*graceBound); ok {
			if end, known := b.deadline(); known && end.Before(at) {
				at, why = end, ErrGraceEnded
			}
		}

		ctx, cancel := context.WithDeadlineCause(ctx, at, why)
		defer cancel()

		err := call(ctx)
		if err != nil && ctx.Err() != nil {
			meta.end(context.Cause(ctx))
		}

		return err
	}
}

func requireRan[S any](at *attemptState[S], meta *callMeta, out *encodedOutput) MiddlewareFunc {
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

func noteCallError(meta *callMeta, err error, progress *attemptProgress) {
	if progress.last.err != nil && !errors.Is(err, progress.last.err) {
		meta.replace(progress.last.err)

		return
	}

	meta.mark(progress.label)
}

func runCall[S any](at *attemptState[S], call func(context.Context, *S) (encodedOutput, error), key string) Call {
	return func(ctx context.Context) error {
		if kind := at.start(); kind != nil {
			at.refuse(kind)

			return Permanent(kind)
		}

		ctx = withIdempotencyKey(ctx, key)

		var (
			label *KindError
			out   encodedOutput
		)

		fresh, err := copyState(at.base)
		if err != nil {
			label, err = ErrInvalidState, Permanent(err)
		} else {
			out, label, err = invoke(ctx, call, fresh)
		}

		at.finish(out, err, label)

		return err
	}
}

func invoke[S any](
	ctx context.Context, call func(context.Context, *S) (encodedOutput, error), fresh *S,
) (encodedOutput, *KindError, error) {
	var out encodedOutput

	panicked, err := recoverPanic(func() error {
		var callErr error

		out, callErr = call(ctx, fresh)

		return callErr
	})

	switch {
	case panicked:
		return encodedOutput{}, ErrPanic, err
	case err != nil:
		return encodedOutput{}, nil, err
	default:
		return out, nil, nil
	}
}

func pipeline[S any](given *callSpec, call func(context.Context, *S) (encodedOutput, error)) callFunc[S] {
	spec := new(*given)

	var timeoutCause error
	if spec.timesOut() {
		timeoutCause = fmt.Errorf("sagas: step %q timed out after %v: %w", spec.step, spec.timeout,
			context.DeadlineExceeded)
	}

	return func(ctx context.Context, meta *callMeta, base snapshot[S]) outcome {
		var output encodedOutput

		info := CallInfo{Saga: spec.saga, Execution: meta.execution, Step: spec.step, Phase: spec.phase}
		key := info.IdempotencyKey()

		attempt := func(ctx context.Context) error {
			at := &attemptState[S]{base: base}

			attemptInfo := info
			attemptInfo.Attempt = meta.attempt

			layers := compose(attemptLayers(spec, meta, at, &output, timeoutCause))

			return layers.Wrap(ctx, attemptInfo, runCall(at, call, key))
		}

		err := withRetry(spec.retry, meta).Wrap(ctx, info, attempt)

		out := outcome{kind: classify(err), attempts: meta.attempt}
		if err == nil {
			out.output = output

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
	return s.timeout > 0
}

func attemptLayers[S any](
	spec *callSpec, meta *callMeta, at *attemptState[S], output *encodedOutput, timeoutCause error,
) []Middleware {
	layers := make([]Middleware, 0, len(spec.middleware)+coreLayers)

	if spec.timesOut() {
		layers = append(layers, withDeadline(meta, spec.timeout, timeoutCause))
	}

	layers = append(layers, recoverer(meta), requireRan(at, meta, output))
	layers = append(layers, spec.middleware...)

	if spec.wrap != nil {
		layers = append(layers, spec.wrap)
	}

	return layers
}
