package sagastest

import (
	"context"
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
func (r *Recorder) Middleware() sagas.MiddlewareFunc {
	return func(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
		err := call(ctx)

		r.mu.Lock()
		defer r.mu.Unlock()

		r.calls = append(r.calls, Call{
			Step: info.Step, Phase: info.Phase, Attempt: info.Attempt, Key: info.IdempotencyKey(), Err: err,
		})

		return err
	}
}

// Calls returns a copy of the recorded calls in the order they returned.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.calls)
}
