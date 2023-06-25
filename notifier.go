package sagas

import (
	"context"
	"sync"
)

// notifier is a struct that contains a list of observers.
// It is responsible for notifying all observers when an event occurs.
type notifier struct {
	observers []*observer
}

// NewNotifier returns a new Notifier.
func NewNotifier() *notifier {
	return &notifier{
		observers: make([]*observer, 0),
	}
}

// Add adds an observer to the Notifier.
func (n *notifier) Add(observer *observer) {
	n.observers = append(n.observers, observer)
}

// Notify send to all observers in parallel that an notification occurred.
func (n *notifier) Notify(ctx context.Context, notification Notification) {
	wg := sync.WaitGroup{}
	for _, obs := range n.observers {
		wg.Add(1)
		go func(o *observer) {
			defer wg.Done()
			o.Execute(ctx, notification)
		}(obs)
	}
	wg.Wait()
}
