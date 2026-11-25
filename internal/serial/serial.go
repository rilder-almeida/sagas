// Package serial marks a context for a run that begins one call at a time.
package serial

import "context"

type key struct{}

// With returns a child of ctx that carries the serial mark.
func With(ctx context.Context) context.Context {
	return context.WithValue(ctx, key{}, true)
}

// On reports whether ctx carries the serial mark.
func On(ctx context.Context) bool {
	return ctx.Value(key{}) != nil
}
