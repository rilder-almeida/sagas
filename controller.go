package sagas

import (
	"context"
)

var (
	eventList = []event{Running, Completed, Canceled, Failed, Successed}
)

type enderFn func() bool

type saga struct {
	starter *Step
	middles []*Step
}

type planner struct {
	identifier identifier
	event      event
	actions    Actions
}

type Controller struct {
	planner  *planner
	expl     *ExecutionPlan
	observer *observer
	notifier *notifier
	saga     *saga
}

func NewController() *Controller {
	return &Controller{
		expl:     NewExecutionPlan(),
		planner:  &planner{},
		notifier: NewNotifier(),
		saga:     &saga{},
	}
}

func (c *Controller) AddSteps(starterStep *Step, steps ...*Step) {
	if starterStep == nil {
		panic("starter step cannot be nil")
	}

	c.saga.starter = starterStep
	c.saga.middles = steps
	c.spreadAllEvents(starterStep)
	if len(steps) != 0 && steps[0] != nil {
		for _, step := range steps {
			c.spreadAllEvents(step)
		}
	}
}

func (c *Controller) When(s *Step) *Controller {
	c.planner.identifier = s.getIdentifier()
	return c
}

func (c *Controller) Is(event event) *Controller {
	c.planner.event = event
	return c
}

func (c *Controller) Then(actions ...Action) *Controller {
	c.planner.actions = actions
	return c
}

func (c *Controller) Plan() *Controller {
	c.expl.Add(notification{
		identifier: c.planner.identifier,
		event:      c.planner.event,
	}, c.planner.actions)
	c.planner = &planner{}
	return c
}

func (c *Controller) Run(ctx context.Context, enderFn enderFn) {
	observer := MustNewObserver(c.expl)
	c.setObserver(observer)
	c.centralizeNorifiers()
	c.saga.starter.Run(ctx)
	for {
		if enderFn() {
			break
		}
	}
}

func (c *Controller) centralizeNorifiers() {
	c.saga.starter.getNotifier().Add(c.getObserver())
	for _, step := range c.saga.middles {
		step.getNotifier().Add(c.getObserver())
	}
}

func (c *Controller) spreadAllEvents(step *Step) {
	for _, event := range eventList {
		c.When(step).Is(event).Then(func(ctx context.Context) error {
			n, _ := NewNotification(step.getIdentifier(), event)
			c.getNotifier().Notify(ctx, n)
			return nil
		}).Plan()
	}
}

func (c *Controller) getNotifier() *notifier {
	return c.notifier
}

func (c *Controller) getObserver() *observer {
	return c.observer
}

func (c *Controller) setObserver(o *observer) {
	c.observer = o
}
