package sagas

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

const (
	// DefaultAttempts is the number of attempts used when RetryPolicy.Attempts is zero.
	DefaultAttempts = 3
	// DefaultInitialDelay is the delay before the second attempt used when RetryPolicy.Initial is zero.
	DefaultInitialDelay = 200 * time.Millisecond
	// DefaultMaxDelay is the delay cap used when RetryPolicy.Max is zero.
	DefaultMaxDelay = 30 * time.Second
	// DefaultMultiplier is the delay growth factor used when RetryPolicy.Multiplier is zero.
	DefaultMultiplier = 2.0
)

// RetryPolicy sets how a step retries a call. The zero value is the default policy.
type RetryPolicy struct {
	// Attempts is the most calls made. Zero means DefaultAttempts.
	Attempts int
	// Initial is the delay before the second attempt. Zero means DefaultInitialDelay.
	Initial time.Duration
	// Max caps every delay. Zero means DefaultMaxDelay.
	Max time.Duration
	// Multiplier is the growth factor of the delay. Zero means DefaultMultiplier. One means a constant delay.
	Multiplier float64
	// NoJitter turns off the random spread of each delay. False, the zero value, draws each delay at random
	// between zero and its computed value.
	NoJitter bool
}

func (p RetryPolicy) attempts() int {
	if p.Attempts > 0 {
		return p.Attempts
	}

	return DefaultAttempts
}

func (p RetryPolicy) initial() time.Duration {
	if p.Initial > 0 {
		return p.Initial
	}

	return DefaultInitialDelay
}

func (p RetryPolicy) maxDelay() time.Duration {
	if p.Max > 0 {
		return p.Max
	}

	return DefaultMaxDelay
}

func (p RetryPolicy) multiplier() float64 {
	if p.Multiplier > 0 {
		return p.Multiplier
	}

	return DefaultMultiplier
}

// NoRetry returns the policy of one attempt and no retry.
func NoRetry() RetryPolicy { return RetryPolicy{Attempts: 1} }

var errPermanent = errors.New("sagas: permanent")

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() error { return e.err }

func (*permanentError) Is(target error) bool { return target == errPermanent }

// Permanent marks err so that the retry loop stops on it, anywhere in an error chain. It returns nil for a nil err.
func Permanent(err error) error {
	if err == nil {
		return nil
	}

	return &permanentError{err: err}
}

func isPermanent(err error) bool { return errors.Is(err, errPermanent) }

func backoff(p RetryPolicy, attempt int, r func(int64) int64) time.Duration {
	delay := p.maxDelay()
	if growth := float64(p.initial()) * math.Pow(p.multiplier(), float64(attempt-1)); growth < float64(delay) {
		delay = time.Duration(growth)
	}

	delay = max(delay, 1)

	if p.NoJitter {
		return delay
	}

	return time.Duration(r(int64(delay)))
}

func withRetry(p RetryPolicy, meta *callMeta) MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error {
		return runAttempts(ctx, p, meta, call, rand.Int64N)
	}
}

func runAttempts(ctx context.Context, p RetryPolicy, meta *callMeta, call Call, draw func(int64) int64) error {
	for attempt := 1; ; attempt++ {
		meta.attempt = attempt

		err := call(ctx)
		if err == nil || !retryable(err) || attempt >= p.attempts() || !sleep(ctx, backoff(p, attempt, draw)) {
			return err
		}

		meta.closeAttempt(err)
	}
}

func retryable(err error) bool {
	var panicked *PanicError

	return !isPermanent(err) && !errors.As(err, &panicked)
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}

	return ctx.Err() == nil
}
