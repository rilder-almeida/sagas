package sagas

import (
	"context"
	"errors"
	"sync"
)

// DefaultEventQueueSize is the number of events that NewMemoryEventQueue holds when its size is zero or less.
const DefaultEventQueueSize = 1024

var (
	errQueueFull   = errors.New("sagas: event queue is full")
	errQueueClosed = errors.New("sagas: event queue is closed")
)

// EventQueue carries the recorded events of a run to the observers that subscribe to it. Enqueue never waits. It
// returns an error when the queue cannot take the event.
type EventQueue interface {
	Enqueue(ctx context.Context, e *Event) error
	Subscribe(o Observer)
}

// MemoryEventQueue is an EventQueue in memory. One goroutine gives each event, in order, to every subscriber.
type MemoryEventQueue struct {
	deliveries chan func(Observer)
	done       chan struct{}

	mu        sync.Mutex
	closed    bool
	observers []Observer
}

// NewMemoryEventQueue returns a queue that holds up to size events, or DefaultEventQueueSize when size is zero or
// less. Its goroutine runs until Close.
func NewMemoryEventQueue(size int) *MemoryEventQueue {
	if size <= 0 {
		size = DefaultEventQueueSize
	}

	q := &MemoryEventQueue{deliveries: make(chan func(Observer), size), done: make(chan struct{})}

	go q.deliver()

	return q
}

// Enqueue adds a copy of e and returns at once, with an error when the queue is full or closed. Each observer gets
// the values of ctx but not its end.
func (q *MemoryEventQueue) Enqueue(ctx context.Context, e *Event) error {
	detached := context.WithoutCancel(ctx)

	stored := *e
	cloneSlices(&stored)

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return errQueueClosed
	}

	select {
	case q.deliveries <- func(o Observer) { o.Observe(detached, &stored) }:
		return nil
	default:
		return errQueueFull
	}
}

// Subscribe adds o to the observers that get each later event.
func (q *MemoryEventQueue) Subscribe(o Observer) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.observers = append(q.observers, o)
}

// Close stops the queue from taking events and waits until every event in it is delivered. If ctx ends first, Close
// returns context.Cause(ctx). Close can run more than once.
func (q *MemoryEventQueue) Close(ctx context.Context) error {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.deliveries)
	}
	q.mu.Unlock()

	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (q *MemoryEventQueue) deliver() {
	defer close(q.done)

	for delivery := range q.deliveries {
		for _, o := range q.subscribers() {
			delivery(o)
		}
	}
}

func (q *MemoryEventQueue) subscribers() []Observer {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.observers
}
