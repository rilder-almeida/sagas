package sagas

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"
)

var (
	// ErrInvalidStep is returned by New for a step or saga option that breaks a rule of the definition.
	ErrInvalidStep = &KindError{name: "invalid-step", message: "sagas: invalid step"}
	// ErrDuplicateStep is returned by New when two steps share a name.
	ErrDuplicateStep = &KindError{name: "duplicate-step", message: "sagas: duplicate step"}
	// ErrCycle is returned by New when the steps depend on each other in a loop.
	ErrCycle = &KindError{name: "cycle", message: "sagas: cycle"}
)

// DefaultRecoveryTimeout is the recovery grace period used when WithRecoveryTimeout is zero or absent.
const DefaultRecoveryTimeout = 5 * time.Minute

const (
	keyWithJournal         = "WithJournal"
	keyWithMiddleware      = "WithMiddleware"
	keyWithRecoveryTimeout = "WithRecoveryTimeout"
	keyWithSecretStore     = "WithSecretStore"
)

// Option configures a saga that New builds.
type Option func(*definition)

type definition struct {
	journal    Journal
	secrets    SecretStore
	middleware []Middleware
	recovery   time.Duration
	given      map[string]int
}

// Saga is a validated, immutable definition that can run many times.
type Saga[S any] struct {
	name        string
	steps       []stepNode[S]
	reports     []StepReport
	journal     Journal
	secrets     SecretStore
	recovery    time.Duration
	order       []int
	ancestors   [][]bool
	outputField []int
	pivot       int
	idField     int
	blockAll    bool
}

type stepNode[S any] struct {
	name         string
	replaces     bool
	pred         func(context.Context, S) bool
	policy       FailurePolicy
	parents      []int
	children     []int
	zone         Zone
	level        int
	action       callFunc[S]
	compensation callFunc[S]
}

// New builds the saga called name that runs flow, or returns every problem of the definition joined.
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
		name: name, journal: d.journal, secrets: d.secrets, recovery: d.recovery, order: in.graph.order,
		ancestors:   in.graph.ancestors,
		outputField: in.fields, pivot: in.graph.pivot, reports: in.reports, idField: in.idField,
		steps: make([]stepNode[S], 0, len(in.reports)),
	}
	if s.recovery == 0 {
		s.recovery = DefaultRecoveryTimeout
	}

	zoned := zones(in.graph.children, s.pivot)

	for i := range in.reports {
		rep := &in.reports[i]
		n := stepNode[S]{
			name: rep.Name, policy: rep.FailurePolicy, parents: in.graph.parents[i],
			pred: in.methods[i].enabled, replaces: in.methods[i].replaces, zone: zoned[i],
		}

		if n.policy == "" {
			n.policy = Block
		}

		n.buildCalls(name, rep, n.zone, &in.methods[i], d.middleware)

		s.blockAll = s.blockAll || n.policy == BlockAll
		s.steps = append(s.steps, n)
	}

	compensable := make([]bool, len(s.steps))
	for i := range s.steps {
		compensable[i] = s.steps[i].compensation != nil
	}

	levels := levelsOf(in.graph.children, s.order, compensable)

	for i := range s.steps {
		s.steps[i].children = in.graph.children[i]
		s.steps[i].level = levels[i]
	}

	return s
}

func (n *stepNode[S]) stepZone() StepZone {
	return StepZone{Name: n.name, Zone: n.zone, Compensable: n.compensation != nil}
}

func (n *stepNode[S]) buildCalls(
	saga string, rep *StepReport, zone Zone, m *stepMethods[S], middleware []Middleware,
) {
	spec := callSpec{
		saga: saga, step: rep.Name, phase: PhaseExecute, retry: rep.RetryPolicy,
		timeout: attemptTimeout(rep.AttemptTimeout), middleware: middleware, wrap: m.wrap,
	}

	switch {
	case rep.Pivot:
		spec.phase = PhasePivot
	case zone == ZoneForwardOnly:
		spec.retry, spec.timeout = rep.RecoveryPolicy.retry(), attemptTimeout(rep.RecoveryPolicy.AttemptTimeout)
	default:
	}

	n.action = pipeline(&spec, m.call)

	if m.compensate == nil {
		return
	}

	spec.phase = PhaseCompensate
	spec.retry, spec.timeout = rep.RecoveryPolicy.retry(), attemptTimeout(rep.RecoveryPolicy.AttemptTimeout)
	n.compensation = pipeline(&spec, m.compensate)
}

func attemptTimeout(d time.Duration) time.Duration {
	if d == 0 {
		return DefaultAttemptTimeout
	}

	return d
}

func checkSaga(name string, d *definition, nilOptions int) []error {
	rules := checkName(name)

	if d.given[keyWithJournal] > 0 && isNilValue(d.journal) {
		rules = append(rules, "WithJournal journal is nil")
	}

	if d.given[keyWithSecretStore] > 0 && isNilValue(d.secrets) {
		rules = append(rules, "WithSecretStore secret store is nil")
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

	rules = append(rules, checkRepeats(d.given, nilOptions)...)

	errs := make([]error, 0, len(rules))
	for _, rule := range rules {
		errs = append(errs, fmt.Errorf("%w: saga %q: %s", ErrInvalidStep, name, rule))
	}

	return errs
}

func checkRepeats(given map[string]int, nilOptions int) []string {
	var rules []string

	if nilOptions > 0 {
		rules = append(rules, "nil Option")
	}

	for _, name := range slices.Sorted(maps.Keys(given)) {
		if given[name] > 1 {
			rules = append(rules, name+" given more than once")
		}
	}

	return rules
}

// WithJournal sets the journal that records each run. Left out, a run uses a fresh in-memory journal.
func WithJournal(j Journal) Option {
	return func(d *definition) {
		d.journal = j
		d.given[keyWithJournal]++
	}
}

// WithSecretStore sets the secret store that keeps the secret values of each run. Left out, a run uses a fresh
// MemorySecretStore, which keeps a secret value only for the length of the run. A durable journal needs a durable
// secret store.
func WithSecretStore(s SecretStore) Option {
	return func(d *definition) {
		d.secrets = s
		d.given[keyWithSecretStore]++
	}
}

// WithMiddleware sets the middlewares that wrap every call of the saga, the first one outermost. Call it once with
// every middleware.
func WithMiddleware(mw ...Middleware) Option {
	return func(d *definition) {
		d.middleware = slices.Clone(mw)
		d.given[keyWithMiddleware]++
	}
}

// WithRecoveryTimeout sets the grace period after the caller's context ends. Zero means DefaultRecoveryTimeout, and the
// bound holds only when every step, compensation and the journal honor their context. A call that returns after the
// grace period can miss the journal, but Run reports it.
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
