package sagas

import (
	"context"
	"log/slog"
	"time"
)

// Slog returns middleware that logs each attempt to l: saga, execution, step, phase, attempt, duration,
// and the error when the attempt fails, at info on success and warn on error. It never logs the state and
// returns the call error unchanged. Outside a pipeline call the step fields are empty.
func Slog(l *slog.Logger) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			start := time.Now()
			err := next(ctx)
			info, _ := Info(ctx)

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

			l.LogAttrs(ctx, level, "sagas: attempt", attrs...)

			return err
		}
	}
}
