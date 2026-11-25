package sagastest

import (
	"context"

	"github.com/rilder-almeida/sagas/internal/serial"
)

// WithoutConcurrency marks ctx so that a run begins one step or compensation at a time. Steps begin in graph order.
// Compensations begin in reverse graph order, except under a BlockAll step in the saga: then they go level by level, in
// reverse graph order within each level.
func WithoutConcurrency(ctx context.Context) context.Context {
	return serial.With(ctx)
}
