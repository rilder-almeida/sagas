package sagas

import (
	"context"
)

// Observer is an interface that represents an observer that is responsible for
// sending the notification to the execution plan through the Execute method.
type Observer interface {
	// Execute executes the given notification through the execution plan.
	Execute(ctx context.Context, notification Notification)
	// getExecutionPlan returns the execution plan.
	getExecutionPlan() ExecutionPlan
}

// observer the concrete implementation of the Observer interface. It is
// responsible for sending the notification to the execution plan through the
// Execute method.
type observer struct {
	executionPlan ExecutionPlan
}

// NewObserver returns a new observer, it receives an execution plan as parameter. A
// panic will occur if the execution plan is nil. It returns an Observer. Example:
//
//	executionPlan := sagas.NewExecutionPlan()
//
//	actionFn := func(ctx context.Context, notification sagas.Notification) {
//		fmt.Println("actionFn")
//	}
//
//	action := sagas.NewAction(actionFn)
//
//	identifier := sagas.Identifier("identifier")
//
//	notification, err := sagas.NewNotification(identifier, sagas.Completed)
//
//	executionPlan.Add(notification, action)
//
//	observer := sagas.NewObserver(executionPlan)
//
//	observer.Execute(context.Background(), notification)
//
// The above example will create a new observer and execute the notification
// through the execution plan.
func NewObserver(executionPlan ExecutionPlan) Observer {

	if executionPlan == nil {
		panic("executionPlan can not be nil")
	}

	return &observer{
		executionPlan: executionPlan,
	}
}

// Execute executes the given notification through the execution plan.
func (o *observer) Execute(ctx context.Context, notification Notification) {
	o.executionPlan.run(ctx, notification)
}

// getExecutionPlan returns the execution plan.
func (o *observer) getExecutionPlan() ExecutionPlan {
	return o.executionPlan
}
