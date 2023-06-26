package sagas

import (
	"context"
	"sync"
)

// ExecutionPlan is the interface that wraps methods to add and run actions. It is used to abstract out the process
// of adding actions to a given notification. Once the notification occurs, all actions of the notification are executed.
type ExecutionPlan interface {
	// Add adds actions to a given notification of a given identifier in the execution plan.
	Add(Notification, ...Action)
	// run executes all actions of a given notification in the execution plan. It runs in parallel.
	// If the notification does not exist in the execution plan, it does nothing.
	run(context.Context, Notification)
}

// ExecutionPlan is a concrete implementation of the ExecutionPlan interface.
type executionPlan struct {
	Plan  Plan
	mutex sync.Mutex
}

// NewExecutionPlan returns a new executionPlan struct. It is used to create a new concrete implementation of the
// ExecutionPlan interface. Example:
//
//	plan := sagas.NewExecutionPlan()
//
// The above example will create a new executionPlan struct.
func NewExecutionPlan() ExecutionPlan {
	return &executionPlan{
		Plan:  newPlan(),
		mutex: sync.Mutex{},
	}
}

// Add is a method that adds actions to a given notification of a given identifier in the execution plan.
// If the notification does not exist in the execution plan, it creates a new notification. Example:
//
//	identifier := sagas.Identifier("identifier")
//
//	notification, err := sagas.NewNotification(identifier, sagas.Completed)
//
//	executionPlan := sagas.NewExecutionPlan()
//
//	action := sagas.NewAction(func(ctx context.Context) error { return nil })
//
//	executionPlan.Add(notification, action)
//
// The above example will create a new notification with the identifier "identifier" and the event Completed.
func (xp *executionPlan) Add(notification Notification, actions ...Action) {
	xp.mutex.Lock()
	defer xp.mutex.Unlock()
	xp.Plan.add(notification.Identifier, notification.Event, actions...)
}

// run is a method that executes all actions of a given notification in the execution plan. It runs in parallel.
// If the notification does not exist in the execution plan, it does nothing.
func (xp *executionPlan) run(ctx context.Context, notification Notification) {

	if actions, ok := xp.Plan.get(notification.Identifier, notification.Event); ok {
		runParallel(ctx, actions, notification)
	}
}

// runParallel executes all actions in parallel and store the result in the Action.
func runParallel(ctx context.Context, actions []Action, notification Notification) {

	// FIXME: The error is not being handled or returned or stored anywhere.
	// This is a problem because the caller of this function will not know if
	// the action failed or not. It occurs because the action should be a Step's
	// Run method, and the Run method has the responsibility to handle the Action's
	// error.

	wg := sync.WaitGroup{}
	for _, a := range actions {
		wg.Add(1)
		go func(a Action) {
			defer wg.Done()
			a.run(ctx)
		}(a)
	}
}
