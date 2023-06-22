package sagas

import (
	"context"
)

// observer is responsible for executing the execution plan.
// It is the entry point that should be used by the application to execute the
// execution plan.
type observer struct {
	ExecutionPlan *ExecutionPlan
}

// MustNewObserver returns a new observer.
func MustNewObserver(executionPlan *ExecutionPlan) *observer {

	if executionPlan == nil {
		// FIXME: Should log.Fatal instead of panic
		panic("executionPlan is nil")
	}

	return &observer{
		ExecutionPlan: executionPlan,
	}
}

// Execute executes the given notification through the execution plan.
func (o *observer) Execute(ctx context.Context, notification notification) {
	o.ExecutionPlan.Run(ctx, notification)
}
