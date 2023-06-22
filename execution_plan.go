package sagas

import (
	"context"
	"errors"
	"sync"
)

// ExecutionPlan is a map of identifier to a map of event to a slice of Action.
// It is used to define which actions of whith step should be executed when an notification occurs.
type ExecutionPlan map[identifier]map[event]Actions

// NewExecutionPlan returns a new ExecutionPlan.
func NewExecutionPlan() *ExecutionPlan {
	return &ExecutionPlan{}
}

// Add adds an action to array of actions of a given event of a given identifier in the execution plan.
func (xp ExecutionPlan) Add(notification notification, actions Actions) {
	if _, ok := xp[notification.identifier]; !ok {
		xp[notification.identifier] = make(map[event]Actions)
	}

	if _, ok := xp[notification.identifier][notification.event]; !ok {
		xp[notification.identifier][notification.event] = make(Actions, 0)
	}

	xp[notification.identifier][notification.event] = append(xp[notification.identifier][notification.event], actions...)
}

// Run executes all actions of a given notification of a given identifier in the execution plan.
func (xp ExecutionPlan) Run(ctx context.Context, notification notification) error {
	if actions, ok := xp.get(notification.identifier, notification.event); ok {
		return xp.runParallel(ctx, actions)
	}
	return nil
}

// get returns the actions of a given event of a given identifier in the execution plan.
func (xp ExecutionPlan) get(identifier identifier, event event) (Actions, bool) {
	if _, ok := xp[identifier]; !ok {
		return nil, false
	}

	if _, ok := xp[identifier][event]; !ok {
		return nil, false
	}

	return xp[identifier][event], true
}

// runParallel executes all actions in parallel and returns an error if any of them returns an error.
func (xp ExecutionPlan) runParallel(ctx context.Context, actions Actions) error {
	errChan := make(chan error, len(actions))
	wg := sync.WaitGroup{}
	for _, action := range actions {
		wg.Add(1)
		go func(action Action) {
			errChan <- action(ctx)
		}(action)
		wg.Done()
	}
	wg.Wait()

	var errList string
	for i := 0; i < len(actions); i++ {
		if err := <-errChan; err != nil {
			errList += err.Error()
			if i < len(actions)-1 {
				errList += "; "
			}
		}
	}

	if len(errList) > 0 {
		errList = "errors while executing actions: " + errList
		return errors.New(errList)
	}

	return nil
}
