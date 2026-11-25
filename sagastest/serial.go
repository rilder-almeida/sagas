package sagastest

import (
	"context"

	"github.com/rilder-almeida/sagas/internal/serial"
)

// WithoutConcurrency marks ctx so that a run begins one step or compensation at a time. Steps begin in graph order and
// compensations in reverse graph order. If a step of the saga uses BlockAll, compensations go level by level, in
// reverse graph order within each level.
func WithoutConcurrency(ctx context.Context) context.Context {
	return serial.With(ctx)
}
