package sagas

import (
	"context"
)

// EndFn is a function that returns a boolean value. It is used to indicate
// when the saga should end.
type EnderFn func() bool

// Saga is an interface that represents a Saga. It is responsible for
// orchestrating the steps and the notifications. It is also responsible for
// running the saga. It is the main interface of the package.
type Saga interface {
	// AddSteps adds the steps to the Saga. It must receive at least a starter
	// step and a middle step. It can receive more than one middle step.
	AddSteps(starterStep Step, steps ...Step)
	// When receives a step as parameter and returns a Saga. It is used to
	// indicate which step will emit the notification.
	When(s Step) Saga
	// Is receives an event as parameter and returns a Saga. It is used to
	// indicate which event will be emitted by the step.
	Is(event Event) Saga
	// Then receives a list of actions as parameter and returns a Saga. It is
	// used to indicate which actions will be executed when the notification
	// occurs.
	Then(actions ...Action) Saga
	// Plan returns a Saga. It is used to indicate that the Saga is ready to
	// run. It must be called after the When, Is and Then methods.
	Plan()
	// Run runs the Saga. It receives a context and an enderFn as parameters.
	// The context is used to cancel the execution of the saga. The enderFn is
	// used to indicate when the saga should end.
	Run(ctx context.Context, enderFn EnderFn)
}

// steps is a struct that represents the steps of the saga. It is composed by
// a starter step and a list of middle steps.
type steps struct {
	starter Step
	middles []Step
}

// newSteps returns a new steps. It is responsible for initializing the steps
// struct.
func newSteps() *steps {
	return &steps{}
}

// planner is a struct that represents the planner of the saga. It is composed
// by an identifier, an event and a list of actions. It is used by the Saga's
// methods When, Is, Then and Plan. It is used to hold the information to be
// used by ExecutionPlan.
type planner struct {
	identifier Identifier
	event      Event
	actions    []Action
}

// newPlanner returns a new planner. It is responsible for initializing the
// planner struct.
func newPlanner() *planner {
	return &planner{}
}

// saga is the concrete implementation of the Saga interface. It is composed
// by an execution plan, an observer, a notifier and a planner.
type saga struct {
	Expl     ExecutionPlan
	Notifier Notifier
	Observer Observer
	Planner  *planner
	Steps    *steps
}

// NewSaga returns a new concrete implementation of the Saga interface.
func NewSaga(options ...SagaOption) Saga {

	sagaOption := newSagasOptions(options...)

	return &saga{
		Expl:     sagaOption.ExecutionPlan,
		Notifier: sagaOption.Notifier,
		Planner:  newPlanner(),
		Steps:    newSteps(),
	}
}

// AddSteps adds the steps to the Saga. It must receive at least a starter
// step and a middle step. It can receive more than one middle step. Example:
//
//	identifier := sagas.Identifier("identifier")
//
//	starterStep := sagas.NewStep(identifier, func(ctx context.Context) error {
//		fmt.Println("starter step")
//		return nil
//	})
//
//	middleStep := sagas.NewStep(identifier, func(ctx context.Context) error {
//		fmt.Println("middle step")
//		return nil
//	})
//
//	saga := sagas.NewSaga()
//
//	saga.AddSteps(starterStep, middleStep)
//
// The above example will create a new saga and add the steps to it. Then it
// will run the saga until the middle step is completed.
func (c *saga) AddSteps(starterStep Step, steps ...Step) {
	if starterStep == nil {
		panic("starter step cannot be nil")
	}

	c.Steps.starter = starterStep
	c.Steps.middles = steps

	c.spreadAllEvents(starterStep)
	if len(steps) != 0 && steps[0] != nil {
		for _, step := range steps {
			c.spreadAllEvents(step)
		}
	}
}

// When receives a step as parameter and returns a Saga. It is used to
// indicate which step will emit the notification.
func (c *saga) When(s Step) Saga {
	c.Planner.identifier = s.GetIdentifier()
	return c
}

// Is receives an event as parameter and returns a Saga. It is used to
// indicate which event will be spectated by the step.
func (c *saga) Is(event Event) Saga {
	c.Planner.event = event
	return c
}

// Then receives a list of actions as parameter and returns a Saga. It is
// used to indicate which actions will be executed when the notification
func (c *saga) Then(actions ...Action) Saga {
	c.Planner.actions = actions
	return c
}

// Plan returns a Saga. It is used to indicate that the Saga is ready to
// run. It must be called after the When, Is and Then methods.
func (c *saga) Plan() {
	c.Expl.Add(Notification{
		Identifier: c.Planner.identifier,
		Event:      c.Planner.event,
	}, c.Planner.actions...)
	c.Planner = newPlanner()
}

// Run runs the Saga. It receives a context and an enderFn as parameters.
// The context is used to cancel the execution of the saga. The enderFn is
// used to indicate when the saga should end.
func (c *saga) Run(ctx context.Context, enderFn EnderFn) {
	c.Observer = NewObserver(c.Expl)
	c.centralizeNorifiers()
	c.Steps.starter.Run(ctx)
	for {
		if enderFn() {
			break
		}
	}
}

func (c *saga) centralizeNorifiers() {
	c.Steps.starter.getNotifier().Add(c.Observer)
	for _, step := range c.Steps.middles {
		step.getNotifier().Add(c.Observer)
	}
}

func (c *saga) spreadAllEvents(step Step) {
	for _, event := range callableEventList {
		c.When(step).Is(event).Then(NewAction(func(ctx context.Context) error {
			n, _ := NewNotification(step.GetIdentifier(), event)
			c.Notifier.Notify(ctx, n)
			return nil
		})).Plan()
	}
}
