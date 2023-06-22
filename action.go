package sagas

import (
	"context"
)

// Action is a function that receives a context and returns an Output and an error. It is made in time of execution by an ActionFn.
// It is used to execute a Step's, Compensation's or Reversion's process.
type Action func(context.Context) error

// Actions is a slice of Action.
type Actions []Action
