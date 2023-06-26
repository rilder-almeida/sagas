package sagas

import "errors"

// Classifier is the interface to classify errors. It is used to determine
// whether an action should be retried or not.
type Classifier interface {
	// Classify receives an error and returns a Status. If the error is nil, it
	// returns Successed; if the error is not nil, it returns Retry.
	Classify(error) Status
}

type classifier struct{}

// NewClassifier creates a new default classifier. It is the default
// classifier used if no classifier is provided. If the error is nil, it
// returns Successed, otherwise it returns Retry. Example:
//
//	classifier := sagas.NewClassifier()
//
// The above example will create a new default classifier that will return Retry if the error is not nil.
func NewClassifier() Classifier {
	return classifier{}
}

// Classify implements the classifier interface for the default classifier.
func (c classifier) Classify(err error) Status {
	if err == nil {
		return Successed
	}

	return retry
}

type classifierWhitelist []error

// NewClassifierWhitelist creates a new whitelist classifier. If the error is nil, it
// returns Successed; if the error is in the whitelist, it returns Retry; otherwise, it returns Failed. Example:
//
//	classifier := sagas.NewClassifierWhitelist(errors.New("error"))
//
// The above example will create a new whitelist classifier that will return Retry if the error is errors.New("error").
func NewClassifierWhitelist(errors ...error) Classifier {
	return classifierWhitelist(errors)
}

// Classify implements the classifier interface for the whitelist classifier.
func (list classifierWhitelist) Classify(err error) Status {
	if err == nil {
		return Successed
	}

	for _, pass := range list {
		if errors.Is(err, pass) {
			return retry
		}
	}

	return Failed
}

type classifierBlacklist []error

// NewClassifierBlacklist creates a new blacklist classifier. If the error is nil, it
// returns Successed; if the error is in the blacklist, it returns Failed; otherwise, it returns Retry. Example:
//
//	classifier := sagas.NewClassifierBlacklist(errors.New("error"))
//
// The above example will create a new blacklist classifier that will return Failed if the error is errors.New("error").
func NewClassifierBlacklist(errors ...error) Classifier {
	return classifierBlacklist(errors)
}

// Classify implements the classifier interface for the blacklist classifier.
func (list classifierBlacklist) Classify(err error) Status {
	if err == nil {
		return Successed
	}

	for _, pass := range list {
		if errors.Is(err, pass) {
			return Failed
		}
	}

	return retry
}
