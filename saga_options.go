package sagas

type sagaOptions struct {
	ExecutionPlan ExecutionPlan
	Notifier      Notifier
}

type SagaOption func(*sagaOptions)

func newSagasOptions(opts ...SagaOption) *sagaOptions {
	opt := &sagaOptions{
		ExecutionPlan: NewExecutionPlan(),
		Notifier:      NewNotifier(),
	}

	for _, o := range opts {
		o(opt)
	}

	return opt
}

// WithSagaExecutionPlan sets the execution plan to the saga.
func WithSagaExecutionPlan(plan ExecutionPlan) SagaOption {
	return func(o *sagaOptions) {
		o.ExecutionPlan = plan
	}
}

// WithSagaNotifier sets the notifier to the saga.
func WithSagaNotifier(notifier Notifier) SagaOption {
	return func(o *sagaOptions) {
		o.Notifier = notifier
	}
}
