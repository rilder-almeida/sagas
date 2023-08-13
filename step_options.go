package sagas

type stepOptions struct {
	Retrier  Retrier
	Status   Status
	State    State
	Notifier Notifier
}

type StepOption func(*stepOptions)

func newStepOptions(opts ...StepOption) stepOptions {
	options := stepOptions{
		Retrier:  nil,
		Status:   Undefined,
		State:    Idle,
		Notifier: NewNotifier(),
	}

	for _, opt := range opts {
		opt(&options)
	}

	return options
}

func WithStepRetrier(retrier Retrier) StepOption {
	return func(o *stepOptions) {
		o.Retrier = retrier
	}
}

func WithStepStatus(status Status) StepOption {
	return func(o *stepOptions) {
		o.Status = status
	}
}

func WithStepState(state State) StepOption {
	return func(o *stepOptions) {
		o.State = state
	}
}

func WithStepNotifier(notifier Notifier) StepOption {
	return func(o *stepOptions) {
		o.Notifier = notifier
	}
}
