package sagas

import (
	"context"
)

// observer is responsible for executing the execution plan.
// It is the entry point that should be used by the application to execute the
// execution plan.
type observer struct {
	executionPlan *executionPlan
}

// NewObserver returns a new observer.
func NewObserver(executionPlan *executionPlan) *observer {

	if executionPlan == nil {
		// FIXME: Should log.Fatal instead of panic
		panic("executionPlan is nil")
	}

	return &observer{
		executionPlan: executionPlan,
	}
}

// Execute executes the given notification through the execution plan.
func (o *observer) Execute(ctx context.Context, notification notification) {
	o.executionPlan.Run(ctx, notification)
}
