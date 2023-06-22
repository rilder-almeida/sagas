package sagas

import "errors"

// classifier is the interface implemented by anything that can classify Errors for a Retriable.
type classifier interface {
	Classify(error) Status
}

type defaultClassifier struct{}

// NewDefaultClassifier creates a new default classifier. It is the default
// classifier used if no classifier is provided. If the error is nil, it
// returns Successed, otherwise it returns Retry.
func NewDefaultClassifier() classifier {
	return defaultClassifier{}
}

// Classify implements the classifier interface.
func (c defaultClassifier) Classify(err error) Status {
	if err == nil {
		return Successed
	}

	return Retry
}

type whitelistClassifier []error

// NewWhitelistClassifier creates a new whitelist classifier. If the error is nil, it
// returns Successed; if the error is in the whitelist, it returns Retry; otherwise, it returns Failed.
func NewWhitelistClassifier(errors ...error) classifier {
	return whitelistClassifier(errors)
}

// Classify implements the classifier interface.
func (list whitelistClassifier) Classify(err error) Status {
	if err == nil {
		return Successed
	}

	for _, pass := range list {
		if errors.Is(err, pass) {
			return Retry
		}
	}

	return Failed
}

type blacklistClassifier []error

// NewBlacklistClassifier creates a new blacklist classifier. If the error is nil, it
// returns Successed; if the error is in the blacklist, it returns Failed; otherwise, it returns Retry.
func NewBlacklistClassifier(errors ...error) classifier {
	return blacklistClassifier(errors)
}

// Classify implements the classifier interface.
func (list blacklistClassifier) Classify(err error) Status {
	if err == nil {
		return Successed
	}

	for _, pass := range list {
		if errors.Is(err, pass) {
			return Failed
		}
	}

	return Retry
}
