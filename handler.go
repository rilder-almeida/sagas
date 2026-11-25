package sagas

import "context"

// Call runs one call of a step method, or the rest of a middleware chain around it.
type Call func(ctx context.Context) error

// Middleware wraps the calls of a step. Its Wrap must run call with ctx or a context derived from it, return only after
// every call it made returns, and never run call concurrently.
type Middleware interface {
	Wrap(ctx context.Context, info CallInfo, call Call) error
}
