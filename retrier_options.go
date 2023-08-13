package sagas

type retrierOptions struct {
	Classifier Classifier
}

type RetrierOption func(*retrierOptions)

func newRetrierOptions(opts ...RetrierOption) retrierOptions {
	options := retrierOptions{
		Classifier: NewClassifier(),
	}

	for _, opt := range opts {
		opt(&options)
	}

	return options
}

func WithRetrierClassifier(classifier Classifier) RetrierOption {
	return func(o *retrierOptions) {
		o.Classifier = classifier
	}
}
