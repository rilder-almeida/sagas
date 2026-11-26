package sagas

import "context"

// Observer gets each event after the journal recorded it. The event is a copy for that observer only. Observe runs
// apart from the run, so a slow observer does not delay it.
type Observer interface {
	Observe(ctx context.Context, e *Event)
}
