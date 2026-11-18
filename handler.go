package sagas

import "context"

// Handler runs one call of an action or a compensation.
type Handler func(ctx context.Context) error

// Middleware wraps a Handler and returns the wrapped Handler. The wrapped Handler passes next the context
// it received, or one derived from it. A context that lost the call state fails the call. The wrapped
// Handler returns only after every next call it made returns, and it never calls next concurrently.
type Middleware func(next Handler) Handler
