package sagas

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Step holds the name and settings of a step. A step is a pointer to a user type that embeds Step and declares
// Execute or Pivot, and optionally Compensate, Enabled and Wrap. Step methods must be safe for concurrent use.
// An empty Name takes the type name; a saga with a durable journal sets Name, so a type rename keeps the names.
type Step struct {
	Name                string
	Description         string
	Retry               RetryPolicy
	AttemptTimeout      time.Duration
	CompensationFailure CompensationFailurePolicy
}

// CompensationFailurePolicy sets what a saga does when a step's compensation fails. The empty value means
// CompensationBlock.
type CompensationFailurePolicy string

const (
	// CompensationBlock stops the compensation of the saga at the failed compensation. It is the default.
	CompensationBlock CompensationFailurePolicy = "block"
	// CompensationContinue goes on with the compensations that remain.
	CompensationContinue CompensationFailurePolicy = "continue"
	// CompensationHalt stops the whole saga at the failed compensation.
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

func checkRepeats(given map[string]int, nilOptions int, kind string) []string {
	var rules []string

	if nilOptions > 0 {
		rules = append(rules, "nil "+kind)
	}

	for _, name := range slices.Sorted(maps.Keys(given)) {
		if given[name] > 1 {
			rules = append(rules, name+" given more than once")
		}
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
