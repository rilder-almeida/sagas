package sagas

import (
	"context"
	"log/slog"
	"time"
)

// Slog is middleware that logs each attempt to Logger, or to slog.Default() when Logger is nil. It never logs the
// state.
type Slog struct {
	Logger *slog.Logger
}

// Wrap logs the attempt of call and returns the call error unchanged.
func (s Slog) Wrap(ctx context.Context, info CallInfo, call Call) error {
	start := time.Now()
	err := call(ctx)

	attrs := []slog.Attr{
		slog.String("saga", info.Saga),
		slog.String("execution", info.Execution),
		slog.String("step", info.Step),
		slog.String("phase", string(info.Phase)),
		slog.Int("attempt", info.Attempt),
		slog.Duration("duration", time.Since(start)),
	}

	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn

		attrs = append(attrs, slog.Any("error", err))
	}

	s.logger().LogAttrs(ctx, level, "sagas: attempt", attrs...)

	return err
}

func (s Slog) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}

	return s.Logger
}
