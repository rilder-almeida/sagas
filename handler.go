package sagas

import "context"

// Call runs one call of a step method, or the rest of a middleware chain around it.
type Call func(ctx context.Context) error

// Middleware wraps the calls of a step. Wrap runs call with ctx or a context derived from it, returns only
// after every call it made returns, and never runs call concurrently. It gets the data of the call as info.
type Middleware interface {
	Wrap(ctx context.Context, info CallInfo, call Call) error
}
