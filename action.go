package sagas

import (
	"context"
)

// ActionFn is a function that receives a context and returns an Output and an error. It is made in time of execution by an ActionFn.
// It is used to execute a Step's, Compensation's or Reversion's process.
type ActionFn func(context.Context) error

type Action struct {
	actionFn ActionFn
	result   error
}

// NewAction returns a new Action.
func NewAction(actionFn ActionFn) *Action {
	return &Action{
		actionFn: actionFn,
		result:   nil,
	}
}

func (a *Action) run(ctx context.Context) error {
	return a.actionFn(ctx)
}

func (a *Action) getResult() error {
	return a.result
}
