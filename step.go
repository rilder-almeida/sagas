package sagas

import (
	"fmt"
	"strings"
	"time"
)

// Step holds a step's name and settings: a pointer to a type that embeds Step and has Execute or Pivot, and optionally
// Compensate, Enabled and Wrap, all safe for concurrent use and idempotent. Execute, Pivot, Compensate and Wrap honor
// their context. A call that returns after the grace period may miss the journal, but Run reports it. An empty Name
// takes the type name in kebab case. Set Name for a durable journal so a rename keeps it. New cannot see a method lost
// to a same-name field or to an embedded field that also embeds a provider of it. Declare methods on the step type.
type Step struct {
	Name        string
	Description string
	// Retry governs the action and the compensation, so under NoRetry the compensation gets one attempt. More
	// attempts make a compensation persist, bounded by the caller's context, then by WithRecoveryTimeout.
	Retry               RetryPolicy
	AttemptTimeout      time.Duration
	CompensationFailure CompensationFailurePolicy
}

// CompensationFailurePolicy sets what a saga does when a step's compensation fails. The empty value means
// CompensationBlock.
type CompensationFailurePolicy string

const (
	// CompensationBlock blocks every compensation that waits on the failed one; other branches go on. It is the
	// default.
	CompensationBlock CompensationFailurePolicy = "block"
	// CompensationContinue goes on with the compensations that remain.
	CompensationContinue CompensationFailurePolicy = "continue"
	// CompensationHalt makes a failed compensation of this step stop every compensation that did not start.
	CompensationHalt CompensationFailurePolicy = "halt"
)

const nameRule = `name may hold only ASCII letters, digits, "-", "_" and "." and must start with a letter or a digit`

func checkName(name string) []string {
	if name == "" {
		return []string{"empty name"}
	}

	valid := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}

	for i := range len(name) {
		if c := name[i]; !valid(c) && (i == 0 || !strings.ContainsRune("-_.", rune(c))) {
			return []string{nameRule}
		}
	}

	return nil
}

func checkSettings(s *StepReport) []string {
	var rules []string

	if s.AttemptTimeout < 0 {
		rules = append(rules, "AttemptTimeout is negative")
	}

	rules = append(rules, checkRetry(s.Retry)...)

	switch s.CompensationFailure {
	case "", CompensationBlock, CompensationContinue, CompensationHalt:
	default:
		rules = append(rules, fmt.Sprintf("unknown compensation failure policy %q", string(s.CompensationFailure)))
	}

	if s.CompensationFailure != "" && !s.Compensate {
		rules = append(rules, "CompensationFailure without Compensate")
	}

	return rules
}

func checkRetry(p RetryPolicy) []string {
	var rules []string

	if p.Attempts < 0 {
		rules = append(rules, "Retry.Attempts is negative")
	}

	if p.Initial < 0 {
		rules = append(rules, "Retry.Initial is negative")
	}

	if p.Max < 0 {
		rules = append(rules, "Retry.Max is negative")
	}

	switch {
	case p.Multiplier < 0:
		rules = append(rules, "Retry.Multiplier is negative")
	case p.Multiplier > 0 && p.Multiplier < 1:
		rules = append(rules, "Retry.Multiplier is below 1")
	default:
	}

	if p.Initial >= 0 && p.Max >= 0 && p.maxDelay() < p.initial() {
		rules = append(rules, "Retry.Max is below Retry.Initial after defaults")
	}

	return rules
}
