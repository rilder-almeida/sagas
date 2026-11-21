package sagas

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
)

// StepReport describes one step of a flow as New sees it: its settings, the methods its type has, and After, the
// names of the steps it waits for.
type StepReport struct {
	Name                string
	Description         string
	Retry               RetryPolicy
	AttemptTimeout      time.Duration
	CompensationFailure CompensationFailurePolicy
	Execute             bool
	Pivot               bool
	Compensate          bool
	Enabled             bool
	Wrap                bool
	After               []string
}

type stepMethods[S any] struct {
	call       func(context.Context, *S) error
	compensate func(context.Context, *S) error
	enabled    func(*S) bool
	wrap       Middleware
}

type namedStep struct {
	name    string
	typ     reflect.Type
	derived bool
}

type inspection[S any] struct {
	reports []StepReport
	methods []stepMethods[S]
	graph   graph
}

// Inspect runs the checks of New on flow for the state type S. It reports each step in the order it first appears
// in flow, or returns every problem joined.
func Inspect[S any](flow Flow) ([]StepReport, error) {
	in, err := examine[S](flow)
	if err != nil {
		return nil, err
	}

	return in.reports, nil
}

func examine[S any](flow Flow) (inspection[S], error) {
	r := resolve(flow)
	errs := r.errs

	if len(r.flows) == 0 {
		errs = append(errs, fmt.Errorf("%w: saga has no steps", ErrInvalidStep))
	}

	in := inspection[S]{reports: make([]StepReport, len(r.flows)), methods: make([]stepMethods[S], len(r.flows))}
	named := make([]namedStep, len(r.flows))

	for i, f := range r.flows {
		var stepErrs []error

		named[i] = nameStep(f)
		in.reports[i], in.methods[i], stepErrs = inspectStep[S](f, named[i])
		errs = append(errs, stepErrs...)
	}

	errs = append(errs, indexSteps(named)...)

	for i, parents := range r.parents {
		for _, p := range parents {
			in.reports[i].After = append(in.reports[i].After, in.reports[p].Name)
		}
	}

	var graphErrs []error

	in.graph, graphErrs = checkGraph(in.reports, r.parents)
	errs = append(errs, graphErrs...)

	state := reflect.TypeFor[S]()
	errs = append(errs, checkState(state), checkExecutionID(state))

	return in, errors.Join(errs...)
}

func nameStep(f Flow) namedStep {
	t := reflect.TypeOf(f)
	if name := f.flowStep().Name; name != "" {
		return namedStep{name: name, typ: t}
	}

	base := t
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}

	return namedStep{name: nameOf(base.Name()), typ: t, derived: true}
}

func nameOf(typeName string) string {
	base, _, _ := strings.Cut(typeName, "[")
	runes := []rune(base)

	var b strings.Builder

	for i, c := range runes {
		lowerNext := i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if i > 0 && unicode.IsUpper(c) && (!unicode.IsUpper(runes[i-1]) || lowerNext) {
			b.WriteByte('-')
		}

		b.WriteRune(unicode.ToLower(c))
	}

	return b.String()
}

func inspectStep[S any](f Flow, n namedStep) (StepReport, stepMethods[S], []error) {
	settings := *f.flowStep()
	v := reflect.ValueOf(f)

	rules := checkName(n.name)
	if len(rules) > 0 && n.derived {
		rules[0] += "; set Name"
	}

	var (
		m       stepMethods[S]
		execute func(context.Context, *S) error
		pivot   func(context.Context, *S) error
		wrap    func(context.Context, CallInfo, Call) error
	)

	hasExecute := method(v, "Execute", &execute, &rules)
	hasPivot := method(v, "Pivot", &pivot, &rules)
	hasCompensate := method(v, "Compensate", &m.compensate, &rules)
	hasEnabled := method(v, "Enabled", &m.enabled, &rules)
	hasWrap := method(v, "Wrap", &wrap, &rules)

	switch execPresent, pivotPresent := present(v, "Execute"), present(v, "Pivot"); {
	case !execPresent && !pivotPresent:
		rules = append(rules, fmt.Sprintf("type %s has neither Execute nor Pivot", v.Type()))
	case execPresent && pivotPresent:
		rules = append(rules, fmt.Sprintf("type %s has both Execute and Pivot", v.Type()))
	default:
	}

	report := StepReport{
		Name: n.name, Description: settings.Description, Retry: settings.Retry,
		AttemptTimeout: settings.AttemptTimeout, CompensationFailure: settings.CompensationFailure,
		Execute: hasExecute, Pivot: hasPivot, Compensate: hasCompensate, Enabled: hasEnabled, Wrap: hasWrap,
	}

	rules = append(rules, ambiguousWrap(v.Type())...)
	rules = append(rules, checkSettings(&report)...)

	m.call = execute
	if hasPivot {
		m.call = pivot
	}

	if hasWrap {
		m.wrap = MiddlewareFunc(wrap)
	}

	errs := make([]error, 0, len(rules))
	for _, rule := range rules {
		errs = append(errs, fmt.Errorf("%w: step %q: %s", ErrInvalidStep, n.name, rule))
	}

	return report, m, errs
}

func present(v reflect.Value, name string) bool {
	return v.MethodByName(name).IsValid()
}

func method[F any](v reflect.Value, name string, dst *F, rules *[]string) bool {
	m := v.MethodByName(name)
	if !m.IsValid() {
		return false
	}

	f, ok := reflect.TypeAssert[F](m)
	if !ok {
		*rules = append(*rules, fmt.Sprintf("method %s of type %s is %s, want %s",
			name, v.Type(), m.Type(), reflect.TypeFor[F]()))

		return false
	}

	*dst = f

	return true
}

func ambiguousWrap(t reflect.Type) []string {
	if _, ok := t.MethodByName("Wrap"); ok || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return nil
	}

	var fields []string

	for f := range t.Elem().Fields() {
		ft := f.Type
		if ft.Kind() != reflect.Pointer {
			ft = reflect.PointerTo(ft)
		}

		if _, ok := ft.MethodByName("Wrap"); f.Anonymous && ok {
			fields = append(fields, f.Name)
		}
	}

	if len(fields) <= 1 {
		return nil
	}

	return []string{fmt.Sprintf("type %s declares no Wrap and embeds one in %s", t, strings.Join(fields, " and "))}
}

func checkExecutionID(t reflect.Type) error {
	m := reflect.New(t).MethodByName("ExecutionID")
	if !m.IsValid() || m.Type() == reflect.TypeFor[func() string]() {
		return nil
	}

	name := t.Name()
	if name == "" {
		name = t.String()
	}

	return fmt.Errorf("%w: %s: method ExecutionID is %s, want func() string", ErrInvalidState, name, m.Type())
}
