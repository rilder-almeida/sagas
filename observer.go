package sagas

import (
	"context"
)

// observer is responsible for executing the execution plan.
// It is the entry point that should be used by the application to execute the
// execution plan.
type observer struct {
	executionPlan *ExecutionPlan
}

// NewObserver returns a new observer.
func NewObserver(executionPlan *ExecutionPlan) *observer {

	if executionPlan == nil {
		panic("executionPlan can not be nil")
	}

	return &observer{
		executionPlan: executionPlan,
	}
}

// Execute executes the given notification through the execution plan.
func (o *observer) Execute(ctx context.Context, notification Notification) {
	o.executionPlan.Run(ctx, notification)
}
