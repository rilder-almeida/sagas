package sagas

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// Retrier is the interface that wraps methods to retry an action.
type Retrier interface {
	Retry(context.Context, actionFn) error
}

// retrier implements the "retriable" resiliency pattern, abstracting out the process of retrying a failed action
// a certain number of times with an optional back-off between each retry.
type retrier struct {
	// backoff is the amount of time to wait before each retry. The length of the slice indicates how many times
	// an action will be retried, and the value at each index indicates the amount of time waited before each.
	backoff []time.Duration
	// classifier is used to determine which errors should be retried and which should cause the retrier to fail fast.
	classifier classifier
	// random is used to randomize the backoff time.
	random *rand.Rand
	// mutex is used to protect the random number generator.
	mutex sync.Mutex
}

// NewRetrier constructs a Retrier with the given backoff pattern and classifier. The length of the backoff pattern
// indicates how many times an action will be retried, and the value at each index indicates the amount of time
// waited before each subsequent retry. The classifier is used to determine which errors should be retried and
// which should cause the retrier to fail fast. The DefaultClassifier is used if nil is passed.
func NewRetrier(backoff []time.Duration, classifier classifier) Retrier {
	if classifier == nil {
		classifier = defaultClassifier{}
	}

	return &retrier{
		backoff:    backoff,
		classifier: classifier,
		random:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Retry executes the given work function, then classifies its return value based on the classifier used
// to construct the Retrier. If the result is Succeed or Fail, the return value of the work function is
// returned to the caller. If the result is Retry, then Retry sleeps according to the its backoff policy
// before retrying. If the total number of retries is exceeded then the return value of the work function
// is returned to the caller regardless.
func (r *retrier) Retry(ctx context.Context, action actionFn) error {
	return r.retryCtx(ctx, action)
}

// retryCtx executes the given work function with context
func (r *retrier) retryCtx(ctx context.Context, action actionFn) error {
	retries := 0
	for {
		err := action(ctx)

		switch r.classifier.Classify(err) {
		case Successed, Failed:
			return err
		case Retry:
			if retries >= len(r.backoff) {
				return err
			}

			timeout := time.After(r.calcSleep(retries))
			if err = r.sleep(ctx, timeout); err != nil {
				return err
			}

			retries++
		}
	}
}

func (r *retrier) sleep(ctx context.Context, t <-chan time.Time) error {
	select {
	case <-t:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *retrier) calcSleep(i int) time.Duration {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.backoff[i] + time.Duration(((r.random.Float64()*2)-1)*float64(r.backoff[i]))
}
