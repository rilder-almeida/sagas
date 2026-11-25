package sagas

import (
	"fmt"
	"strings"
)

// FailurePolicy sets what a saga does when a step's compensation fails. The empty value means Block.
type FailurePolicy string

const (
	// Block blocks every compensation that waits on the failed one. Other branches go on. It is the default.
	Block FailurePolicy = "block"
	// Continue goes on with the compensations that remain.
	Continue FailurePolicy = "continue"
	// Halt makes a failed compensation of this step stop every compensation that did not start.
	Halt FailurePolicy = "halt"
)

const nameRule = `name may hold only ASCII letters, digits, "-", "_" and "." and must start with a letter or a digit`

func checkName(name string) ruleList {
	if name == "" {
		return ruleList{"empty name"}
	}

	valid := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}

	for i := range len(name) {
		if c := name[i]; !valid(c) && (i == 0 || !strings.ContainsRune("-_.", rune(c))) {
			return ruleList{nameRule}
		}
	}

	return nil
}

func checkSettings(s *StepReport) ruleList {
	var rules ruleList

	if s.AttemptTimeout < 0 {
		rules = append(rules, "AttemptTimeout is negative")
	}

	rules = append(rules, checkRetry(s.RetryPolicy)...)

	switch s.FailurePolicy {
	case "", Block, Continue, Halt:
	default:
		rules = append(rules, fmt.Sprintf("unknown compensation failure policy %q", string(s.FailurePolicy)))
	}

	if s.FailurePolicy != "" && !s.Compensate {
		rules = append(rules, "FailurePolicy without Compensate")
	}

	return rules
}

func checkRetry(p RetryPolicy) ruleList {
	var rules ruleList

	if p.Attempts < 0 {
		rules = append(rules, "RetryPolicy.Attempts is negative")
	}

	if p.Initial < 0 {
		rules = append(rules, "RetryPolicy.Initial is negative")
	}

	if p.Max < 0 {
		rules = append(rules, "RetryPolicy.Max is negative")
	}

	switch {
	case p.Multiplier < 0:
		rules = append(rules, "RetryPolicy.Multiplier is negative")
	case p.Multiplier > 0 && p.Multiplier < 1:
		rules = append(rules, "RetryPolicy.Multiplier is below 1")
	default:
	}

	if p.Initial >= 0 && p.Max >= 0 && p.maxDelay() < p.initial() {
		rules = append(rules, "RetryPolicy.Max is below RetryPolicy.Initial after defaults")
	}

	return rules
}
