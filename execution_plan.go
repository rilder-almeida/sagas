package sagas

import (
	"context"
	"sync"
)

// ExecutionPlan is a map of identifier to a map of event to a slice of Action.
// It is used to define which actions of whith step should be executed when an notification occurs.
type ExecutionPlan struct {
	Plan  Plan
	mutex sync.Mutex
}

// NewExecutionPlan returns a new executionPlan.
func NewExecutionPlan() *ExecutionPlan {
	return &ExecutionPlan{
		Plan:  newPlan(),
		mutex: sync.Mutex{},
	}
}

// Add adds an Action to array of actions of a given event of a given identifier in the execution plan.
func (xp *ExecutionPlan) Add(notification Notification, actions ...Action) {
	xp.mutex.Lock()
	defer xp.mutex.Unlock()
	xp.Plan.add(notification.Identifier, notification.Event, actions...)
}

// Run executes all actions of a given notification of a given identifier in the execution plan.
func (xp *ExecutionPlan) Run(ctx context.Context, notification Notification) {

	if actions, ok := xp.Plan.get(notification.Identifier, notification.Event); ok {
		runParallel(ctx, actions, notification)
	}
}

// runParallel executes all actions in parallel and store the result in the Action.
func runParallel(ctx context.Context, actions []Action, notification Notification) {
	errCh := make(chan error, len(actions))

	wg := sync.WaitGroup{}
	for _, a := range actions {
		wg.Add(1)
		go func(a Action) {
			defer wg.Done()
			errCh <- a.run(ctx)
		}(a)
	}
	wg.Wait()
	close(errCh)
}
