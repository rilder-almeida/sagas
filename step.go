package sagas

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
)

const (
	keyCompensate            = "Compensate"
	keyIf                    = "If"
	keyTimeout               = "Timeout"
	keyRetry                 = "Retry"
	keyOnCompensationFailure = "OnCompensationFailure"
	keyPivot                 = "Pivot"
	keyAfter                 = "After"
)

// StepOption configures one step of a saga. Pass it to Step.
type StepOption func(*stepDef)

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

type stepDef struct {
	name       string
	do         any
	undo       any
	pred       any
	timeout    time.Duration
	retry      RetryPolicy
	policy     CompensationFailurePolicy
	after      []string
	pivot      bool
	given      map[string]int
	nilOptions int
}

// Step declares a step named name that runs do. A step name is unique and not empty.
func Step[S any](name string, do func(context.Context, *S) error, opts ...StepOption) Option {
	return func(d *definition) {
		s := stepDef{name: name, do: do, given: map[string]int{}}

		for _, opt := range opts {
			if opt == nil {
				s.nilOptions++

				continue
			}

			opt(&s)
		}

		d.steps = append(d.steps, s)
	}
}

// Compensate sets the function that undoes the step. A step without it has nothing to undo.
func Compensate[S any](undo func(context.Context, *S) error) StepOption {
	return func(s *stepDef) {
		s.undo = undo
		s.given[keyCompensate]++
	}
}

// If makes the step conditional: it runs only when pred returns true for the state. A step without it always runs.
func If[S any](pred func(*S) bool) StepOption {
	return func(s *stepDef) {
		s.pred = pred
		s.given[keyIf]++
	}
}

// Timeout limits each attempt of the step's action to d. Zero, the default, sets no limit.
func Timeout(d time.Duration) StepOption {
	return func(s *stepDef) {
		s.timeout = d
		s.given[keyTimeout]++
	}
}

// Retry sets how the step retries its action. The zero policy is the default policy.
func Retry(p RetryPolicy) StepOption {
	return func(s *stepDef) {
		s.retry = p
		s.given[keyRetry]++
	}
}

// OnCompensationFailure sets what happens when the step's compensation fails. The empty policy means
// CompensationBlock. It needs Compensate on the same step.
func OnCompensationFailure(p CompensationFailurePolicy) StepOption {
	return func(s *stepDef) {
		s.policy = p
		s.given[keyOnCompensationFailure]++
	}
}

// Pivot marks the step as the point of no return. Steps after it cannot be compensated. A saga has at most one.
func Pivot() StepOption {
	return func(s *stepDef) {
		s.pivot = true
		s.given[keyPivot]++
	}
}

// After makes the step wait for the steps called names. Call it once with every name.
func After(names ...string) StepOption {
	return func(s *stepDef) {
		s.after = names
		s.given[keyAfter]++
	}
}

type stepFuncs[S any] struct {
	do   func(context.Context, *S) error
	undo func(context.Context, *S) error
	pred func(*S) bool
}

func (f *stepFuncs[S]) convert(d *stepDef) []string {
	var rules, more []string

	rules = convertFunc(&f.do, d.do, "action")

	if d.undo != nil {
		more = convertFunc(&f.undo, d.undo, "Compensate function")
		rules = append(rules, more...)
	}

	if d.pred != nil {
		more = convertFunc(&f.pred, d.pred, "If function")
		rules = append(rules, more...)
	}

	return rules
}

func checkStep[S any](d *stepDef) (stepFuncs[S], []error) {
	rules := checkName(d.name)

	var funcs stepFuncs[S]

	rules = append(rules, funcs.convert(d)...)

	if d.timeout < 0 {
		rules = append(rules, "Timeout is negative")
	}

	rules = append(rules, checkRetry(d.retry)...)
	rules = append(rules, checkPolicy(d)...)
	rules = append(rules, checkAfter(d.after)...)
	rules = append(rules, checkRepeats(d.given, d.nilOptions, "StepOption")...)

	errs := make([]error, 0, len(rules))

	for _, rule := range rules {
		errs = append(errs, fmt.Errorf("%w: step %q: %s", ErrInvalidStep, d.name, rule))
	}

	return funcs, errs
}

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

func convertFunc[F any](dst *F, v any, label string) []string {
	if v == nil {
		return []string{label + " is nil"}
	}

	f, ok := v.(F)
	if !ok {
		return []string{label + " is for another state type"}
	}

	if reflect.ValueOf(f).IsNil() {
		return []string{label + " is nil"}
	}

	*dst = f

	return nil
}

func checkPolicy(d *stepDef) []string {
	var rules []string

	switch d.policy {
	case "", CompensationBlock, CompensationContinue, CompensationHalt:
	default:
		rules = append(rules, fmt.Sprintf("unknown compensation failure policy %q", string(d.policy)))
	}

	if d.given[keyOnCompensationFailure] > 0 && d.undo == nil {
		rules = append(rules, "OnCompensationFailure without Compensate")
	}

	return rules
}

func checkAfter(after []string) []string {
	var rules []string

	for i, name := range after {
		first := slices.Index(after, name)
		if first < i && !slices.Contains(after[first+1:i], name) {
			rules = append(rules, fmt.Sprintf("After lists %q more than once", name))
		}
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
