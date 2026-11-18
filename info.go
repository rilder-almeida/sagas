package sagas

import "context"

type callMeta struct {
	execution string
	attempt   int
}

type metaKey struct{}

func withMeta(ctx context.Context, m *callMeta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

func metaFrom(ctx context.Context) *callMeta {
	if m, ok := ctx.Value(metaKey{}).(*callMeta); ok {
		return m
	}

	return nil
}

// Phase tells whether a call runs a step's action or its compensation. The empty string means not set.
type Phase string

// Phase values.
const (
	PhaseAction       Phase = "action"
	PhaseCompensation Phase = "compensation"
)

// StepInfo describes the call a handler runs in. Key is the same for the action and the compensation of a step.
type StepInfo struct {
	Saga      string
	Execution string
	Step      string
	Phase     Phase
	Attempt   int
	Key       string
}

type infoKey struct{}

// Info returns the call metadata in ctx, and false outside a pipeline call.
func Info(ctx context.Context) (StepInfo, bool) {
	si, ok := ctx.Value(infoKey{}).(StepInfo)

	return si, ok
}

// Key returns the idempotency key of the call in ctx, and an empty string outside a pipeline call.
func Key(ctx context.Context) string {
	si, _ := Info(ctx)

	return si.Key
}

func withStepInfo(saga, step string, phase Phase) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			var m callMeta
			if p := metaFrom(ctx); p != nil {
				m = *p
			}

			si := StepInfo{
				Saga:      saga,
				Execution: m.execution,
				Step:      step,
				Phase:     phase,
				Attempt:   m.attempt,
				Key:       m.execution + "/" + step,
			}

			return next(context.WithValue(ctx, infoKey{}, si))
		}
	}
}
