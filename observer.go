package sagas

import (
	"context"
	"log/slog"
)

// Observer gets each event after the journal recorded it. The event is a copy for that observer only. Observe runs
// apart from the run, so a slow observer does not delay it.
type Observer interface {
	Observe(ctx context.Context, e *Event)
}

type sagaDeliverer struct {
	saga      string
	observers []Observer
}

func (s *Saga[S]) observe(d *definition) {
	s.queue = d.queue
	if len(d.observers) == 0 {
		return
	}

	if s.queue == nil {
		s.queue = NewMemoryEventQueue()
	}

	s.queue.Subscribe(&sagaDeliverer{saga: s.name, observers: d.observers})
}

func (d *sagaDeliverer) Observe(ctx context.Context, e *Event) {
	if e.Saga != d.saga {
		return
	}

	for _, o := range d.observers {
		own := *e
		cloneSlices(&own)

		panicked, err := recoverPanic(func() error {
			o.Observe(ctx, &own)

			return nil
		})
		if panicked {
			slog.Default().ErrorContext(ctx, "sagas: observer panicked", slog.String("saga", e.Saga),
				slog.String("execution", e.Execution), slog.Int("seq", e.Seq), slog.Any("error", err))
		}
	}
}

func (r *runner[S]) publish(ctx context.Context, e *Event) {
	if r.saga.queue == nil {
		return
	}

	if err := r.saga.queue.Enqueue(ctx, e); err != nil {
		slog.Default().WarnContext(ctx, "sagas: event not observed", slog.String("execution", e.Execution),
			slog.Int("seq", e.Seq), slog.Any("error", err))
	}
}
