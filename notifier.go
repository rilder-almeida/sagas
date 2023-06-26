package sagas

import (
	"context"
	"sync"
)

// Notifier is an interface that represents a Notifier. It is responsible for
// notifying the observers that a notification occurred.
type Notifier interface {
	// Add adds an observer to the Notifier.
	Add(observer Observer)
	// Notify send to all observers in parallel that an notification occurred.
	Notify(ctx context.Context, notification Notification)
}

// notifier is the concrete implementation of the Notifier interface.
type notifier struct {
	observers []Observer
}

// NewNotifier returns a new notifier. It returns a Notifier.
// Example:
//
//	identifier := sagas.Identifier("identifier")
//
//	notification, err := sagas.NewNotification(identifier, sagas.Completed)
//
//	executionPlan := sagas.NewExecutionPlan()
//
//	observer := sagas.NewObserver(executionPlan)
//
//	notifier := sagas.NewNotifier()
//
//	notifier.Add(observer)
//
//	notifier.Notify(context.Background(), notification)
//
// The above example will create a new notifier and notify the observers that a
// notification occurred.
func NewNotifier() Notifier {
	return &notifier{
		observers: make([]Observer, 0),
	}
}

// Add adds an observer to the Notifier. Example:
func (n *notifier) Add(observer Observer) {
	n.observers = append(n.observers, observer)
}

// Notify send to all observers in parallel that an notification occurred.
func (n *notifier) Notify(ctx context.Context, notification Notification) {
	wg := sync.WaitGroup{}
	for _, obs := range n.observers {
		wg.Add(1)
		go func(o Observer) {
			defer wg.Done()
			o.Execute(ctx, notification)
		}(obs)
	}
	wg.Wait()
}
