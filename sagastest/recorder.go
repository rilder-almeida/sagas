package sagastest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/rilder-almeida/sagas"
)

// Call is one recorded call of a step method. Err is the error the call returned, nil on success.
type Call struct {
	Step    string
	Phase   sagas.Phase
	Attempt int
	Key     string
	Err     error
}

// Recorder records every call that passes through its middleware. It is safe for concurrent use.
type Recorder struct {
	mu    sync.Mutex
	calls []Call
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Middleware returns the middleware that records each call after it returns, and returns its error unchanged.
// A call that panics is recorded with an error that wraps sagas.ErrPanic, then panics again with the same value.
func (r *Recorder) Middleware() sagas.MiddlewareFunc {
	return func(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
		defer func() {
			if v := recover(); v != nil {
				r.record(info, panicError(v))
				panic(v)
			}
		}()

		err := call(ctx)
		r.record(info, err)

		return err
	}
}

// Calls returns a copy of the recorded calls in the order they returned.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.calls)
}

func (r *Recorder) record(info sagas.CallInfo, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, Call{
		Step: info.Step, Phase: info.Phase, Attempt: info.Attempt, Key: info.IdempotencyKey(), Err: err,
	})
}

func panicError(v any) error {
	if err, ok := v.(error); ok {
		return fmt.Errorf("%w: %w", sagas.ErrPanic, err)
	}

	return fmt.Errorf("%w: %v", sagas.ErrPanic, v)
}
