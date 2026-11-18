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
