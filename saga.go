package sagas

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

var (
	// ErrInvalidStep is returned by New for a step or saga option that breaks a rule of the definition.
	ErrInvalidStep = errors.New("sagas: invalid step")
	// ErrDuplicateStep is returned by New when two steps share a name.
	ErrDuplicateStep = errors.New("sagas: duplicate step")
	// ErrUnknownDependency is returned by New when a step depends on a step that does not exist.
	ErrUnknownDependency = errors.New("sagas: unknown dependency")
	// ErrCycle is returned by New when the steps depend on each other in a loop.
	ErrCycle = errors.New("sagas: cycle")
)

// DefaultRecoveryTimeout is the recovery grace period used when WithRecoveryTimeout is zero or absent.
const DefaultRecoveryTimeout = 5 * time.Minute

// Option configures a saga. Pass it to New.
type Option func(*definition)

type definition struct {
	steps      []stepDef
	journal    Journal
	middleware []Middleware
	recovery   time.Duration
	given      map[string]int
}

// Saga is a validated, immutable definition that can run many times.
type Saga[S any] struct {
	name     string
	steps    []stepNode[S]
	journal  Journal
	recovery time.Duration
}

type stepNode[S any] struct {
	name    string
	pred    func(*S) bool
	retry   RetryPolicy
	timeout time.Duration
	policy  CompensationFailurePolicy
}

// New builds the saga called name from its options. It returns every problem of the definition, joined.
func New[S any](name string, opts ...Option) (*Saga[S], error) {
	d := &definition{given: map[string]int{}}
	nilOptions := 0

	for _, opt := range opts {
		if opt == nil {
			nilOptions++

			continue
		}

		opt(d)
	}

	errs := checkSaga(name, d, nilOptions)

	for i := range d.steps {
		errs = append(errs, checkStep[S](&d.steps[i])...)
	}

	if err := checkState(reflect.TypeFor[S]()); err != nil {
		errs = append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return build[S](name, d), nil
}

func build[S any](name string, d *definition) *Saga[S] {
	s := &Saga[S]{name: name, journal: d.journal, recovery: d.recovery, steps: make([]stepNode[S], 0, len(d.steps))}
	if s.recovery == 0 {
		s.recovery = DefaultRecoveryTimeout
	}

	for i := range d.steps {
		sd := &d.steps[i]
		n := stepNode[S]{name: sd.name, retry: sd.retry, timeout: sd.timeout, policy: sd.policy}

		if n.policy == "" {
			n.policy = CompensationBlock
		}

		if pred, ok := sd.pred.(func(*S) bool); ok {
			n.pred = pred
		}

		s.steps = append(s.steps, n)
	}

	return s
}

func checkSaga(name string, d *definition, nilOptions int) []error {
	var rules []string

	if name == "" {
		rules = append(rules, "empty name")
	}

	if len(d.steps) == 0 {
		rules = append(rules, "saga has no steps")
	}

	if d.given["WithJournal"] > 0 && isNilValue(d.journal) {
		rules = append(rules, "WithJournal journal is nil")
	}

	if d.recovery < 0 {
		rules = append(rules, "WithRecoveryTimeout is negative")
	}

	for _, mw := range d.middleware {
		if mw == nil {
			rules = append(rules, "nil Middleware in WithMiddleware")

			break
		}
	}

	rules = append(rules, checkRepeats(d.given, nilOptions, "Option")...)

	errs := make([]error, 0, len(rules))
	for _, rule := range rules {
		errs = append(errs, fmt.Errorf("%w: saga %q: %s", ErrInvalidStep, name, rule))
	}

	return errs
}

// WithJournal sets the journal that records each run. Left out, a run uses a fresh in-memory journal.
func WithJournal(j Journal) Option {
	return func(d *definition) {
		d.journal = j
		d.given["WithJournal"]++
	}
}

// WithMiddleware sets the middlewares that wrap every call of the saga. Call it once with every middleware.
func WithMiddleware(mw ...Middleware) Option {
	return func(d *definition) {
		d.middleware = mw
		d.given["WithMiddleware"]++
	}
}

// WithRecoveryTimeout sets the grace period after the caller's context ends. Zero, the default, means
// DefaultRecoveryTimeout.
func WithRecoveryTimeout(t time.Duration) Option {
	return func(d *definition) {
		d.recovery = t
		d.given["WithRecoveryTimeout"]++
	}
}

func nilableKinds() []reflect.Kind {
	return []reflect.Kind{reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan}
}

func isNilValue(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	return slices.Contains(nilableKinds(), rv.Kind()) && rv.IsNil()
}
