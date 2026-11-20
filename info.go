package sagas

import (
	"context"
	"fmt"
	"slices"
)

type callMeta struct {
	execution string
	attempt   int
	previous  []error
	replaced  []error
	cause     error
	label     *KindError
}

func (m *callMeta) mark(label *KindError) {
	if m != nil {
		m.label = label
	}
}

func (m *callMeta) replace(original error) {
	if m != nil {
		m.replaced = append(m.replaced, original)
	}
}

func (m *callMeta) end(cause error) {
	if m != nil && m.cause == nil {
		m.cause = cause
	}
}

func (m *callMeta) history() []error {
	if m == nil {
		return nil
	}

	return slices.Concat(m.previous, m.replaced)
}

func (m *callMeta) closeAttempt(err error) {
	if m == nil {
		return
	}

	if m.cause != nil {
		err = fmt.Errorf("%w: %w", err, m.cause)
	}

	m.previous = slices.Concat(m.previous, m.replaced, []error{err})
	m.replaced, m.cause, m.label = nil, nil, nil
}

func (m *callMeta) kind() *KindError {
	if m == nil {
		return nil
	}

	return m.label
}

// Phase tells which step method a call runs. The empty string means not set.
type Phase string

// Phase values.
const (
	PhaseExecute    Phase = "execute"
	PhasePivot      Phase = "pivot"
	PhaseCompensate Phase = "compensate"
)

// CallInfo describes one call of a step method. Attempt counts from 1.
type CallInfo struct {
	Saga      string
	Execution string
	Step      string
	Phase     Phase
	Attempt   int
}

// IdempotencyKey returns the key of the call, the same in every phase of a step in one execution. It equals
// what the step method reads with the package function IdempotencyKey.
func (c CallInfo) IdempotencyKey() string {
	return c.Execution + "/" + c.Step
}

type idempotencyKey struct{}

func withIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyKey{}, key)
}

// IdempotencyKey returns the idempotency key of the step call in ctx, and an empty string outside a call.
// The key is the same in Execute, Pivot and Compensate, so a compensation finds the effect to undo with it.
// A service that needs its own key on the undo request gets the key plus a suffix, such as "/compensate".
func IdempotencyKey(ctx context.Context) string {
	if key, ok := ctx.Value(idempotencyKey{}).(string); ok {
		return key
	}

	return ""
}
