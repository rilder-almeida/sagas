package sagas

import (
	"context"
	"sync"
)

// executionPlan is a map of identifier to a map of event to a slice of Action.
// It is used to define which actions of whith step should be executed when an notification occurs.
type executionPlan struct {
	plan  plan
	mutex sync.Mutex
}

type plan map[identifier]map[event][]*Action

// NewExecutionPlan returns a new executionPlan.
func NewExecutionPlan() *executionPlan {
	return &executionPlan{
		plan:  make(plan),
		mutex: sync.Mutex{},
	}
}

// Add adds an action to array of actions of a given event of a given identifier in the execution plan.
func (xp *executionPlan) Add(notification notification, actions ...*Action) {
	xp.mutex.Lock()
	defer xp.mutex.Unlock()
	if _, ok := xp.plan[notification.identifier]; !ok {
		xp.plan[notification.identifier] = make(map[event][]*Action)
	}

	if _, ok := xp.plan[notification.identifier][notification.event]; !ok {
		xp.plan[notification.identifier][notification.event] = make([]*Action, 0)
	}

	xp.plan[notification.identifier][notification.event] = append(xp.plan[notification.identifier][notification.event], actions...)
}

// Run executes all actions of a given notification of a given identifier in the execution plan.
func (xp *executionPlan) Run(ctx context.Context, notification notification) {
	if actions, ok := xp.getActions(notification.identifier, notification.event); ok {
		xp.runParallel(ctx, actions)
	}
}

// get returns the actions of a given event of a given identifier in the execution plan.
func (xp *executionPlan) getActions(identifier identifier, event event) ([]*Action, bool) {
	xp.mutex.Lock()
	defer xp.mutex.Unlock()
	if _, ok := xp.plan[identifier]; !ok {
		return nil, false
	}

	if _, ok := xp.plan[identifier][event]; !ok {
		return nil, false
	}

	return xp.plan[identifier][event], true
}

// runParallel executes all actions in parallel and store the result in the action.
func (xp *executionPlan) runParallel(ctx context.Context, actions []*Action) {
	xp.mutex.Lock()
	defer xp.mutex.Unlock()
	wg := sync.WaitGroup{}
	for _, action := range actions {
		wg.Add(1)
		go func(a *Action) {
			defer wg.Done()
			a.result = a.run(ctx)
		}(action)
	}
	wg.Wait()
}
