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
	ErrInvalidStep = &KindError{name: "invalid_step", message: "sagas: invalid step"}
	// ErrDuplicateStep is returned by New when two steps share a name.
	ErrDuplicateStep = &KindError{name: "duplicate_step", message: "sagas: duplicate step"}
	// ErrCycle is returned by New when the steps depend on each other in a loop.
	ErrCycle = &KindError{name: "cycle", message: "sagas: cycle"}
)

// DefaultRecoveryTimeout is the recovery grace period used when WithRecoveryTimeout is zero or absent.
const DefaultRecoveryTimeout = 5 * time.Minute

const (
	keyWithJournal         = "WithJournal"
	keyWithMiddleware      = "WithMiddleware"
	keyWithRecoveryTimeout = "WithRecoveryTimeout"
)

// Option configures a saga. Pass it to New.
type Option func(*definition)

type definition struct {
	journal    Journal
	middleware []Middleware
	recovery   time.Duration
	given      map[string]int
}

// Saga is a validated, immutable definition that can run many times.
type Saga[S any] struct {
	name     string
	steps    []stepNode[S]
	reports  []StepReport
	journal  Journal
	recovery time.Duration
	order    []int
	pivot    int
}

type stepNode[S any] struct {
	name         string
	pred         func(*S) bool
	policy       CompensationFailurePolicy
	parents      []int
	children     []int
	zone         Zone
	action       callFunc[S]
	compensation callFunc[S]
}

// New builds the saga called name that runs flow. It returns every problem of the definition joined, including
// every error Inspect returns for flow.
func New[S any](name string, flow Flow, opts ...Option) (*Saga[S], error) {
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

	in, inspectErr := examine[S](flow)
	if err := errors.Join(append(errs, inspectErr)...); err != nil {
		return nil, err
	}

	return build(name, d, &in), nil
}

func build[S any](name string, d *definition, in *inspection[S]) *Saga[S] {
	s := &Saga[S]{
		name: name, journal: d.journal, recovery: d.recovery, order: in.graph.order,
		pivot: in.graph.pivot, reports: in.reports,
		steps: make([]stepNode[S], 0, len(in.reports)),
	}
	if s.recovery == 0 {
		s.recovery = DefaultRecoveryTimeout
	}

	for i := range in.reports {
		rep := &in.reports[i]
		n := stepNode[S]{
			name: rep.Name, policy: rep.CompensationFailure, parents: in.graph.parents[i], pred: in.methods[i].enabled,
		}

		if n.policy == "" {
			n.policy = CompensationBlock
		}

		n.buildCalls(name, rep, &in.methods[i], d.middleware)

		s.steps = append(s.steps, n)
	}

	zoned := zones(in.graph.children, s.pivot)

	for i := range s.steps {
		s.steps[i].children = in.graph.children[i]
		s.steps[i].zone = zoned[i]
	}

	return s
}

func (n *stepNode[S]) buildCalls(saga string, rep *StepReport, m *stepMethods[S], middleware []Middleware) {
	spec := callSpec{
		saga: saga, step: rep.Name, phase: PhaseExecute, retry: rep.Retry, timeout: rep.AttemptTimeout,
		middleware: middleware, wrap: m.wrap,
	}
	if rep.Pivot {
		spec.phase = PhasePivot
	}

	n.action = pipeline(&spec, m.call)

	if m.compensate == nil {
		return
	}

	spec.phase = PhaseCompensate
	n.compensation = pipeline(&spec, m.compensate)
}

func checkSaga(name string, d *definition, nilOptions int) []error {
	rules := checkName(name)

	if d.given[keyWithJournal] > 0 && isNilValue(d.journal) {
		rules = append(rules, "WithJournal journal is nil")
	}

	if d.recovery < 0 {
		rules = append(rules, "WithRecoveryTimeout is negative")
	}

	for _, mw := range d.middleware {
		if isNilValue(mw) {
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
		d.given[keyWithJournal]++
	}
}

// WithMiddleware sets the middlewares that wrap every call of the saga, the first one outermost. Call it
// once with every middleware.
func WithMiddleware(mw ...Middleware) Option {
	return func(d *definition) {
		d.middleware = slices.Clone(mw)
		d.given[keyWithMiddleware]++
	}
}

// WithRecoveryTimeout sets the grace period after the caller's context ends. Zero, the default, means
// DefaultRecoveryTimeout.
func WithRecoveryTimeout(t time.Duration) Option {
	return func(d *definition) {
		d.recovery = t
		d.given[keyWithRecoveryTimeout]++
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
