package sagastest

import (
	"context"
	"errors"
	"runtime/debug"
	"slices"
	"sync"

	"github.com/rilder-almeida/sagas"
)

// Attempt is one recorded call of a step method. Err is the error that the call returned. For a call that
// panics, Err wraps sagas.ErrPanic and the *sagas.PanicError.
type Attempt struct {
	sagas.CallInfo

	Err error
}

// Recorder is middleware that records every call that passes through it. It is safe for concurrent use.
type Recorder struct {
	mu       sync.Mutex
	attempts []Attempt
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Wrap records the call and returns its error unchanged. A panic is recorded and then raised again.
func (r *Recorder) Wrap(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
	defer func() {
		if v := recover(); v != nil {
			r.record(info, panicError(v))
			panic(v)
		}
	}()

	err := call(ctx)
	r.record(info, markPanic(err))

	return err
}

// Attempts returns a copy of the recorded attempts in the order they returned.
func (r *Recorder) Attempts() []Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.attempts)
}

func (r *Recorder) record(info sagas.CallInfo, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.attempts = append(r.attempts, Attempt{CallInfo: info, Err: err})
}

type recordedPanicError struct {
	err error
}

func (e *recordedPanicError) Error() string { return e.err.Error() }

func (e *recordedPanicError) Unwrap() []error { return []error{sagas.ErrPanic, e.err} }

func markPanic(err error) error {
	if _, ok := errors.AsType[*sagas.PanicError](err); ok {
		return &recordedPanicError{err: err}
	}

	return err
}

func panicError(v any) error {
	return &recordedPanicError{err: &sagas.PanicError{Value: v, Stack: debug.Stack()}}
}
