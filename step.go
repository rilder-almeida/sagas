package sagas

import (
	"context"
	"errors"
)

// Step is an interface that represents a abstract implementation of a step. Step is a unit of work that can be executed and retried.
// It is the basic building block of a Saga and can be composed together to form a Saga.
type Step interface {
	// GetIdentifier returns the unique identifier for the Step.
	GetIdentifier() Identifier
	// GetStatus returns the current status of the Step.
	GetStatus() Status
	// GetState returns the current status of the Step.
	GetState() State
	// Run executes the Step's actionFn and returns the result. If the Step has a retrier,
	Run(context.Context) error
	// getNotifier returns the notifier that will be used to notify events that occur in the Step.
	getNotifier() Notifier
}

// step is the concrete implementation of the Step interface.
type step struct {
	// identifier is a unique identifier for the Step.
	identifier Identifier
	// actionFn is the function that will be executed returning
	// a action that will be executed.
	action Action
	// retrier is the function that can be executed to retry a failed
	retrier Retrier
	// status is the current status of the Step.
	status Status
	// state is the current state of the Step.
	state State
	// notifier is the notifier that will be used to notify events
	notfier Notifier
}

// NewStep creates a new Step with the given name and actionFn. The name is used to identify the Step.
// The actionFn is used to execute the Step.
func NewStep(name string, action ActionFn, options ...StepOption) Step {
	if action == nil {
		panic(errors.New("action cannot be nil"))
	}

	if name == "" {
		panic(errors.New("name cannot be empty"))
	}

	stepOptions := newStepOptions(options...)

	return &step{
		identifier: NewIdentifier(name),
		action:     NewAction(action),
		retrier:    stepOptions.Retrier,
		status:     stepOptions.Status,
		state:      stepOptions.State,
		notfier:    stepOptions.Notifier,
	}
}

// GetIdentifier returns the unique identifier for the Step.
func (s *step) GetIdentifier() Identifier {
	return s.identifier
}

// GetStatus returns the current status of the Step.
func (s *step) GetStatus() Status {
	return s.status
}

// GetState returns the current status of the Step.
func (s *step) GetState() State {
	return s.state
}

// Run executes the Step's actionFn and returns the result. If the Step has a retrier,
// it will be used to retry the actionFn if it fails. If the Step fails, it will be
// set to a failed state. If the Step succeeds, it will be set to a succeed state.
// If the Step is in a failed state, it can be rollforward. If the Step is in a
// succeed state, it can be rollbackwarded.
func (s *step) Run(ctx context.Context) error {
	defer s.setState(ctx, Completed)
	s.setState(ctx, Running)
	if s.retrier != nil {
		return s.runWithRetry(ctx)
	}
	return s.run(ctx)
}

func (s *step) run(ctx context.Context) error {
	err := s.action.run(ctx)
	if err != nil {
		s.setStatus(ctx, Failed)
		return err
	}

	s.setStatus(ctx, Successed)
	return nil
}

func (s *step) runWithRetry(ctx context.Context) error {
	err := s.retrier.Retry(ctx, s.action)
	if err != nil {
		s.setStatus(ctx, Failed)
		return err
	}

	s.setStatus(ctx, Successed)
	return nil
}

// getNotifier returns the notifier that will be used to notify
func (s *step) getNotifier() Notifier {
	return s.notfier
}

// setStatus sets the status of the Step and notifies the observers that a notification occurred.
func (s *step) setStatus(ctx context.Context, status Status) {
	s.status = status
	notification, _ := NewNotification(s.identifier, status)
	s.notfier.Notify(ctx, notification)
}

// setState sets the state of the Step and notifies the observers that a notification occurred.
func (s *step) setState(ctx context.Context, state State) {
	s.state = state
	notification, _ := NewNotification(s.identifier, state)
	s.notfier.Notify(ctx, notification)
}
