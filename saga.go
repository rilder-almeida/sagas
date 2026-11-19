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

const (
	keyWithJournal         = "WithJournal"
	keyWithMiddleware      = "WithMiddleware"
	keyWithRecoveryTimeout = "WithRecoveryTimeout"
)

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

	funcs := make([]stepFuncs[S], len(d.steps))

	for i := range d.steps {
		var stepErrs []error

		funcs[i], stepErrs = checkStep[S](&d.steps[i])
		errs = append(errs, stepErrs...)
	}

	g, graphErrs := checkGraph(name, d.steps)
	errs = append(errs, graphErrs...)

	if err := checkState(reflect.TypeFor[S]()); err != nil {
		errs = append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return build(name, d, funcs, &g), nil
}

func build[S any](name string, d *definition, funcs []stepFuncs[S], g *graph) *Saga[S] {
	s := &Saga[S]{
		name: name, journal: d.journal, recovery: d.recovery, order: g.order,
		pivot: g.pivot,
		steps: make([]stepNode[S], 0, len(d.steps)),
	}
	if s.recovery == 0 {
		s.recovery = DefaultRecoveryTimeout
	}

	for i := range d.steps {
		sd := &d.steps[i]
		n := stepNode[S]{name: sd.name, policy: sd.policy, parents: g.parents[i]}

		if n.policy == "" {
			n.policy = CompensationBlock
		}

		n.pred = funcs[i].pred
		n.buildCalls(name, sd, &funcs[i], d.middleware)

		s.steps = append(s.steps, n)
	}

	zoned := zones(g.children, s.pivot)

	for i := range s.steps {
		s.steps[i].children = g.children[i]
		s.steps[i].zone = zoned[i]
	}

	return s
}

func (n *stepNode[S]) buildCalls(saga string, sd *stepDef, f *stepFuncs[S], middleware []Middleware) {
	spec := callSpec{
		saga: saga, step: sd.name, phase: PhaseAction, retry: sd.retry, timeout: sd.timeout, middleware: middleware,
	}

	n.action = pipeline(&spec, f.do)

	if f.undo == nil {
		return
	}

	spec.phase = PhaseCompensation
	n.compensation = pipeline(&spec, f.undo)
}

func checkSaga(name string, d *definition, nilOptions int) []error {
	rules := checkName(name)

	if len(d.steps) == 0 {
		rules = append(rules, "saga has no steps")
	}

	if d.given[keyWithJournal] > 0 && isNilValue(d.journal) {
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
		d.given[keyWithJournal]++
	}
}

// WithMiddleware sets the middlewares that wrap every call of the saga. Call it once with every middleware.
func WithMiddleware(mw ...Middleware) Option {
	return func(d *definition) {
		d.middleware = mw
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
