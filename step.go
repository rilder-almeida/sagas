package sagas

import (
	"context"
	"errors"
)

// Step is a unit of work that can be executed, retried, rollforward, rollbackwarded and aborted. It is the basic building block of a Saga and
// can be composed together to form a Saga. The flow is controlled by a Saga's controller, which is responsible performing the necessary actions.
// Steps are executed in the order they are added to a Saga, and the Saga will wait for a Step to complete before executing the next Step. If
// a Step fails, the Saga will attempt to rollforward the Step before executing the next Step. If compensation fails, the Saga will attempt to
// rollbackward the Step before executing the next Step. If reversion fails, the Saga will be aborted.
type Step struct {
	// identifier is a unique identifier for the Step.
	identifier Identifier
	// name is the name of the Step.
	name string
	// actionFn is the function that will be executed returning
	// a action that will be executed.
	action ActionFn
	// retry is the function that can be executed to retry a failed
	retry Retrier
	// status is the current status of the Step.
	status Status
	// state is the current state of the Step.
	state State
	// observer is the observer that will be notified of events
	observer *observer
	// notifier is the notifier that will be used to notify events
	notfier *notifier
}

// NewStep creates a new Step with the given name and actionFn. The name is used to identify the Step.
// The actionFn is used to execute the Step.
func NewStep(name string, action ActionFn, retrier Retrier) *Step {
	if action == nil {
		panic(errors.New("action cannot be nil"))
	}

	if name == "" {
		panic(errors.New("name cannot be empty"))
	}

	return &Step{
		identifier: NewIdentifier(name),
		name:       name,
		action:     action,
		status:     Undefined,
		retry:      retrier,
		state:      Idle,
		notfier:    NewNotifier(),
	}
}

// GetName returns the name of the Step.
func (s *Step) GetName() string {
	return s.name
}

// GetStatus returns the current status of the Step.
func (s *Step) GetStatus() Status {
	return s.status
}

// GetState returns the current status of the Step.
func (s *Step) GetState() State {
	return s.state
}

// GetIdentifier returns the unique identifier for the Step.
func (s *Step) GetIdentifier() Identifier {
	return s.identifier
}

// Run executes the Step's actionFn and returns the result. If the Step has a retrier,
// it will be used to retry the actionFn if it fails. If the Step fails, it will be
// set to a failed state. If the Step succeeds, it will be set to a succeed state.
// If the Step is in a failed state, it can be rollforward. If the Step is in a
// succeed state, it can be rollbackwarded.
func (s *Step) Run(ctx context.Context) error {
	defer s.setState(ctx, Completed)
	s.setState(ctx, Running)
	if s.retry != nil {
		return s.runWithRetry(ctx)
	}
	return s.run(ctx)
}

func (s *Step) run(ctx context.Context) error {
	err := s.action(ctx)
	if err != nil {
		s.setStatus(ctx, Failed)
		return err
	}

	s.setStatus(ctx, Successed)
	return nil
}

func (s *Step) runWithRetry(ctx context.Context) error {
	err := s.retry.Retry(ctx, s.action)
	if err != nil {
		s.setStatus(ctx, Failed)
		return err
	}

	s.setStatus(ctx, Successed)
	return nil
}

// setObserver sets the observer that will be notified of
func (s *Step) setObserver(observer *observer) {
	s.observer = observer
}

// getObserver returns the observer that will be notified of
func (s *Step) getObserver() *observer {
	return s.observer
}

// getNotifier returns the notifier that will be used to notify
func (s *Step) getNotifier() *notifier {
	return s.notfier
}

func (s *Step) setStatus(ctx context.Context, status Status) {
	s.status = status
	notification, _ := NewNotification(s.identifier, status)
	s.notfier.Notify(ctx, notification)
}

func (s *Step) setState(ctx context.Context, state State) {
	s.state = state
	notification, _ := NewNotification(s.identifier, state)
	s.notfier.Notify(ctx, notification)
}
