package sagastest

import (
	"context"

	"github.com/rilder-almeida/sagas/internal/serial"
)

// WithoutConcurrency marks ctx so that a run begins one step or compensation at a time, in graph order forward and in
// reverse order backward.
func WithoutConcurrency(ctx context.Context) context.Context {
	return serial.With(ctx)
}
