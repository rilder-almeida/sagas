package sagas

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
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

	name := make([]rune, 0, len(runes))

	for i, c := range runes {
		lowerNext := i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if i > 0 && unicode.IsUpper(c) && (!unicode.IsUpper(runes[i-1]) || lowerNext) {
			name = append(name, '-')
		}

		name = append(name, unicode.ToLower(c))
	}

	return string(name)
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

	ambiguous := ambiguousMethods(v.Type())
	rules = append(rules, ambiguous.rules...)
	rules = append(rules, kindRules(v, &ambiguous)...)

	report := StepReport{
		Name: n.name, Description: settings.Description, Retry: settings.Retry,
		AttemptTimeout: settings.AttemptTimeout, CompensationFailure: settings.CompensationFailure,
		Execute: hasExecute, Pivot: hasPivot, Compensate: hasCompensate, Enabled: hasEnabled, Wrap: hasWrap,
	}

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

func kindRules(v reflect.Value, a *ambiguity) []string {
	execute := v.MethodByName("Execute").IsValid() || a.names["Execute"]
	pivot := v.MethodByName("Pivot").IsValid() || a.names["Pivot"]

	switch {
	case !execute && !pivot:
		return []string{fmt.Sprintf("type %s has neither Execute nor Pivot", v.Type())}
	case execute && pivot:
		return []string{fmt.Sprintf("type %s has both Execute and Pivot", v.Type())}
	default:
		return nil
	}
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

type ambiguity struct {
	names map[string]bool
	rules []string
}

func ambiguousMethods(t reflect.Type) ambiguity {
	a := ambiguity{names: map[string]bool{}}

	for _, name := range []string{"Execute", "Pivot", "Compensate", "Enabled", "Wrap"} {
		if _, declared := t.MethodByName(name); declared {
			continue
		}

		if fields := promoters(t.Elem(), name); len(fields) > 1 {
			a.names[name] = true
			a.rules = append(a.rules, fmt.Sprintf("type %s declares no %s and embeds one in %s",
				t, name, strings.Join(fields, " and ")))
		}
	}

	return a
}

type embedNode struct {
	t         reflect.Type
	path      string
	ancestors []reflect.Type
}

func promoters(t reflect.Type, name string) []string {
	level := []embedNode{{t: t, ancestors: []reflect.Type{t}}}

	for len(level) > 0 {
		providers, next := scanLevel(level, name)
		if len(providers) > 0 {
			return providers
		}

		level = next
	}

	return nil
}

func scanLevel(level []embedNode, name string) ([]string, []embedNode) {
	var (
		providers []string
		next      []embedNode
	)

	for _, n := range level {
		for f := range n.t.Fields() {
			if !f.Anonymous {
				continue
			}

			path := strings.TrimPrefix(n.path+"."+f.Name, ".")

			switch inner := structOf(f.Type); {
			case provides(f.Type, name):
				providers = append(providers, path)
			case inner != nil && !slices.Contains(n.ancestors, inner):
				next = append(
					next,
					embedNode{t: inner, path: path, ancestors: append(slices.Clone(n.ancestors), inner)},
				)
			default:
			}
		}
	}

	return providers, next
}

func provides(t reflect.Type, name string) bool {
	if !hasMethod(t, name) {
		return false
	}

	inner := structOf(t)
	if inner == nil {
		return true
	}

	for f := range inner.Fields() {
		if f.Anonymous && hasMethod(f.Type, name) {
			return false
		}
	}

	return true
}

func hasMethod(t reflect.Type, name string) bool {
	if t.Kind() != reflect.Interface {
		t = reflect.PointerTo(derefType(t))
	}

	_, ok := t.MethodByName(name)

	return ok
}

func derefType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}

	return t
}

func structOf(t reflect.Type) reflect.Type {
	if t = derefType(t); t.Kind() == reflect.Struct {
		return t
	}

	return nil
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
