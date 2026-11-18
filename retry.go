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
	// NoJitter turns off the random spread of each delay.
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

var errPermanent = errors.New("sagas: permanent")

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() error { return e.err }

func (*permanentError) Is(target error) bool { return target == errPermanent }

// Permanent marks err so that the retry loop stops on it. It returns nil for a nil err.
func Permanent(err error) error {
	if err == nil || isPermanent(err) {
		return err
	}

	return &permanentError{err: err}
}

func isPermanent(err error) bool { return errors.Is(err, errPermanent) }

func backoff(p RetryPolicy, attempt int, r func(int64) int64) time.Duration {
	growth := float64(p.initial()) * math.Pow(p.multiplier(), float64(attempt-1))
	delay := time.Duration(math.Min(growth, float64(p.maxDelay())))

	if p.NoJitter {
		return delay
	}

	return time.Duration(r(int64(delay) + 1))
}

func retry(p RetryPolicy) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context) error {
			return attempts(ctx, p, next)
		}
	}
}

func attempts(ctx context.Context, p RetryPolicy, next Handler) error {
	meta := metaFrom(ctx)

	for attempt := 1; ; attempt++ {
		if meta != nil {
			meta.attempt = attempt
		}

		err := next(ctx)
		if err == nil || !retryable(ctx, err) || attempt >= p.attempts() {
			return err
		}

		if !sleep(ctx, backoff(p, attempt, rand.Int64N)) {
			return err
		}
	}
}

func retryable(ctx context.Context, err error) bool {
	var panicked *PanicError

	return ctx.Err() == nil && !isPermanent(err) && !errors.As(err, &panicked)
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
