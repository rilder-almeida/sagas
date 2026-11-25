package sagastest

import (
	"context"
	"fmt"
	"slices"

	"github.com/rilder-almeida/sagas"
)

type fault struct {
	step     string
	phase    sagas.Phase
	attempts []int
}

// FaultOption narrows when a fault fires.
type FaultOption func(*fault)

// OnPhase fires the fault only in phase p. Left out, it fires in every phase.
func OnPhase(p sagas.Phase) FaultOption {
	return func(f *fault) { f.phase = p }
}

// OnAttempts fires the fault only on the listed attempt numbers. Left out, it fires on every attempt.
func OnAttempts(n ...int) FaultOption {
	return func(f *fault) { f.attempts = slices.Clone(n) }
}

func newFault(step string, opts []FaultOption) *fault {
	f := &fault{step: step}
	for _, opt := range opts {
		opt(f)
	}

	return f
}

func (f *fault) matches(info sagas.CallInfo) bool {
	return info.Step == f.step &&
		(f.phase == "" || info.Phase == f.phase) &&
		(len(f.attempts) == 0 || slices.Contains(f.attempts, info.Attempt))
}

// Fail returns middleware that answers each call of the step called step that the options select with err, without
// running it. The name step must equal CallInfo.Step, and Fail panics on a nil err.
func Fail(step string, err error, opts ...FaultOption) sagas.MiddlewareFunc {
	if err == nil {
		panic("sagastest: Fail needs a non-nil error")
	}

	f := newFault(step, opts)

	return sagas.MiddlewareFunc(func(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
		if f.matches(info) {
			return err
		}

		return call(ctx)
	})
}

// Unknown returns middleware that runs each call of the step called step that the options select and then returns
// context.DeadlineExceeded as a lost reply. A call that returns an error keeps that error.
func Unknown(step string, opts ...FaultOption) sagas.MiddlewareFunc {
	f := newFault(step, opts)

	return sagas.MiddlewareFunc(func(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
		err := call(ctx)
		if err != nil || !f.matches(info) {
			return err
		}

		return context.DeadlineExceeded
	})
}

// Panic returns middleware that runs each call of the step called step that the options select and then panics, so the
// effect is applied and the outcome is unknown. A call that returns an error keeps that error.
func Panic(step string, opts ...FaultOption) sagas.MiddlewareFunc {
	f := newFault(step, opts)

	return sagas.MiddlewareFunc(func(ctx context.Context, info sagas.CallInfo, call sagas.Call) error {
		err := call(ctx)
		if err != nil || !f.matches(info) {
			return err
		}

		panic(fmt.Sprintf("sagastest: injected panic in step %q", info.Step))
	})
}
