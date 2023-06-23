package sagas

import (
	"context"
)

// actionFn is a function that receives a context and returns an Output and an error. It is made in time of execution by an actionFn.
// It is used to execute a Step's, Compensation's or Reversion's process.
type actionFn func(context.Context) error

type Action struct {
	actionFn actionFn
	result   error
}

// NewAction returns a new Action.
func NewAction(actionFn actionFn) *Action {
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
