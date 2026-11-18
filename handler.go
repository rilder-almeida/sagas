package sagas

import "context"

// Handler runs one call of an action or a compensation.
type Handler func(ctx context.Context) error

// Middleware wraps a Handler and returns the wrapped Handler.
type Middleware func(next Handler) Handler
