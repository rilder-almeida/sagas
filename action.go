package sagas

import (
	"context"
)

// Action is an interface that contains a method that receives a context and returns an error.
// It is an abstraction of the action that will be executed by a Step.
type Action interface {
	run(context.Context) error
}

// ActionFn is a function that receives a context and returns an error. Is a function type that
// will be used to create a new Action.
type ActionFn func(context.Context) error

// action is a struct that contains a function that receives a context and returns an error.
// It is a concrete implementation of the Action interface.
type action struct {
	actionFn ActionFn
}

// NewAction is the constructor of defaultAction. It receives an ActionFn and returns a defaultAction struct
// that is a concrete implementation of the Action interface.
// Example:
//
//	actionExampleFn := func(ctx context.Context, input interface{}) func(context.Context) error {
//		return func(ctx context.Context) error {
//			// do something with input
//			return nil
//		}
//	}
//
//	actionExample := NewAction(actionExampleFn)
//
// The NewAction function is used to create a new Action that will be executed by a Step.
func NewAction(actionFn ActionFn) Action {
	return &action{
		actionFn: actionFn,
	}
}

// run is the method that executes the actionFn. Is is private and is used by the Step struct.
func (a action) run(ctx context.Context) error {
	return a.actionFn(ctx)
}
