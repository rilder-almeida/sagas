package sagas

import "time"

// BackoffConstant receives the number of attempts and the amount of time to wait between each attempt and returns a slice
// of time.Duration. It generates a simple back-off strategy of retrying 'attempts' times, and waiting 'amount' time after each one.
func BackoffConstant(attempts int, amount time.Duration) []time.Duration {
	ret := make([]time.Duration, attempts)
	for i := range ret {
		ret[i] = amount
	}
	return ret
}

// BackoffExponential receives the number of attempts and the amount of time to wait between each attempt and returns a slice
// of time.Duration. It generates a simple back-off strategy of retrying 'attempts' times, and doubling the amount of
// time waited after each one.
func BackoffExponential(attempts int, initialAmount time.Duration, rate float64) []time.Duration {
	ret := make([]time.Duration, attempts)
	next := initialAmount
	for i := range ret {
		ret[i] = next
		next *= time.Duration(rate)
	}
	return ret
}

// BackoffLimitedExponential receives the number of attempts, the amount of time to wait between each attempt and the
// limit amount of time to wait between each attempt and returns a slice of time.Duration. Is generates a simple back-off
// strategy of retrying 'attempts' times, and doubling the amount of time waited after each one. If back-off reaches `limitAmount`,
// thereafter back-off will be filled with `limitAmount`.
func BackoffLimitedExponential(attempts int, initialAmount time.Duration, limitAmount time.Duration, rate float64) []time.Duration {
	ret := make([]time.Duration, attempts)
	next := initialAmount
	for i := range ret {
		if next < limitAmount {
			ret[i] = next
			next *= time.Duration(rate)
		} else {
			ret[i] = limitAmount
		}
	}
	return ret
}
