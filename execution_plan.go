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
type result map[identifier]map[string][]string

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
func (xp *executionPlan) Run(ctx context.Context, notification notification) result {
	result := make(result)

	if actions, ok := xp.getActions(notification.identifier, notification.event); ok {
		xp.runParallel(ctx, actions, notification, &result)
	}

	return result
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
func (xp *executionPlan) runParallel(ctx context.Context, actions []*Action, notification notification, result *result) {
	errCh := make(chan error, len(actions))

	wg := sync.WaitGroup{}
	for _, action := range actions {
		wg.Add(1)
		go func(a *Action) {
			defer wg.Done()
			errCh <- a.run(ctx)
		}(action)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			xp.addResult(notification, err.Error(), result)
		}
	}
}

func (xp *executionPlan) addResult(notification notification, err string, result *result) {
	xp.mutex.Lock()
	defer xp.mutex.Unlock()

	if _, ok := (*result)[notification.identifier]; !ok {
		(*result)[notification.identifier] = make(map[string][]string)
	}

	if _, ok := (*result)[notification.identifier][notification.event.String()]; !ok {
		(*result)[notification.identifier][notification.event.String()] = make([]string, 0)
	}

	(*result)[notification.identifier][notification.event.String()] = append((*result)[notification.identifier][notification.event.String()], err)
}
