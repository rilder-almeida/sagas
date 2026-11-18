package sagas

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"time"
)

// ErrCallNotRun is the error of a call that middleware returned from without running the action or compensation.
var ErrCallNotRun = errors.New("sagas: middleware returned without running the call")

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
	ran  bool
	out  *S
}

type callStateKey struct{}

func classify(err error) outcomeKind {
	var panicked *PanicError

	switch {
	case err == nil:
		return outcomeSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.As(err, &panicked):
		return outcomeUnknown
	default:
		return outcomeFailure
	}
}

func recoverer(next Handler) Handler {
	return func(ctx context.Context) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = &PanicError{Value: r, Stack: debug.Stack()}
			}
		}()

		return next(ctx)
	}
}

func timeout(d time.Duration) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()

			return next(ctx)
		}
	}
}

func stateFrom[S any](ctx context.Context) *callState[S] {
	if st, ok := ctx.Value(callStateKey{}).(*callState[S]); ok {
		return st
	}

	return &callState[S]{}
}

func requireRan[S any](next Handler) Handler {
	return func(ctx context.Context) error {
		st := stateFrom[S](ctx)
		st.ran = false

		err := next(ctx)
		if err == nil && !st.ran {
			return Permanent(ErrCallNotRun)
		}

		return err
	}
}

func runCall[S any](call func(context.Context, *S) error) Handler {
	return func(ctx context.Context) error {
		st := stateFrom[S](ctx)

		fresh, err := copyState(st.base)
		if err != nil {
			return Permanent(err)
		}

		st.ran = true

		err = call(ctx, fresh)
		if err != nil {
			return err
		}

		st.out = fresh

		return nil
	}
}

func pipeline[S any](spec *callSpec, call func(context.Context, *S) error) callFunc[S] {
	layers := []Middleware{retry(spec.retry), withStepInfo(spec.saga, spec.step, spec.phase)}
	if spec.phase == PhaseAction && spec.timeout > 0 {
		layers = append(layers, timeout(spec.timeout))
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

		err := chain(context.WithValue(withMeta(ctx, meta), callStateKey{}, st))

		out := outcome[S]{kind: classify(err), attempts: meta.attempt, err: err}
		if err == nil {
			out.state = st.out
		}

		return out
	}
}
