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

// StepReport describes one step of a flow as New sees it: its settings, the methods its type has, and After, the names
// of the steps it waits for.
type StepReport struct {
	Name           string
	Description    string
	RetryPolicy    RetryPolicy
	AttemptTimeout time.Duration
	FailurePolicy  FailurePolicy
	Pivot          bool
	Compensate     bool
	Enabled        bool
	Wrap           bool
	After          []string
}

type stepMethods[S any] struct {
	call       func(context.Context, *S) (encodedOutput, error)
	compensate func(context.Context, *S) (encodedOutput, error)
	replaces   bool
	enabled    func(context.Context, S) bool
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
	fields  []int
	idField int
}

// Inspect runs the checks of New on flow for the state type S and reports each step in the order it first appears. It
// cannot see a step method that a field of the same name or two embedded types hide.
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

	in := inspection[S]{
		reports: make([]StepReport, len(r.flows)), methods: make([]stepMethods[S], len(r.flows)),
		fields: make([]int, len(r.flows)),
	}
	named := make([]namedStep, len(r.flows))
	outputs := make([]reflect.Type, len(r.flows))

	for i, b := range r.flows {
		var (
			stepErrs   []error
			fieldRules ruleList
		)

		named[i], outputs[i] = nameStep(b, &r.settings[i]), b.output()
		in.fields[i], fieldRules = outputField(reflect.TypeFor[S](), outputs[i])
		in.reports[i], in.methods[i], stepErrs = inspectStep[S](b, &r.settings[i], named[i], in.fields[i], fieldRules)
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
	errs = append(errs, concurrentOutputs(in.reports, outputs, &in.graph)...)

	idField, stateErr := examineState(reflect.TypeFor[S]())
	in.idField = idField

	errs = append(errs, stateErr, refuseExecutionIDMethod[S]())

	return in, errors.Join(errs...)
}

func nameStep(b binder, set *stepSettings) namedStep {
	t := reflect.TypeOf(b)
	if name := set.name; name != "" {
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

func inspectStep[S any](
	b binder, set *stepSettings, n namedStep, field int, fieldRules ruleList,
) (StepReport, stepMethods[S], []error) {
	rules := checkName(n.name)
	if len(rules) > 0 && n.derived {
		rules[0] += "; set Name"
	}

	m, bindRules := bindStep[S](b, field, fieldRules)
	rules = append(rules, bindRules...)

	ambiguous := ambiguousMethods(reflect.TypeOf(b))
	rules = append(rules, ambiguous.rules...)

	if !ambiguous.names["Execute"] && !reflect.ValueOf(b).MethodByName("Execute").IsValid() {
		rules = append(rules, fmt.Sprintf("type %T has no Execute", b))
	}

	report := StepReport{
		Name: n.name, Description: set.description, RetryPolicy: set.retry, AttemptTimeout: set.timeout,
		FailurePolicy: set.failure, Pivot: set.pivot, Compensate: m.compensate != nil, Enabled: m.enabled != nil,
		Wrap: m.wrap != nil,
	}

	rules = append(rules, checkSettings(&report)...)

	errs := make([]error, 0, len(rules))
	for _, rule := range rules {
		errs = append(errs, fmt.Errorf("%w: step %q: %s", ErrInvalidStep, n.name, rule))
	}

	return report, m, errs
}

func bindStep[S any](b binder, field int, fieldRules ruleList) (stepMethods[S], ruleList) {
	sb, ok := b.(stateBinder[S])
	if !ok {
		embed, _ := embedsStep(reflect.TypeOf(b))

		mismatch := fmt.Sprintf("type %T embeds %s, want the state %s",
			b, shortType(embed.Type), shortType(reflect.TypeFor[S]()))

		return stepMethods[S]{}, append(ruleList{mismatch}, namedOutput(b.output())...)
	}

	m, rules := sb.bind(b, field)

	return m, append(rules, fieldRules...)
}

type ambiguity struct {
	names map[string]bool
	rules []string
}

func ambiguousMethods(t reflect.Type) ambiguity {
	a := ambiguity{names: map[string]bool{}}

	for _, name := range []string{"Execute", "Compensate", "Enabled", "Wrap"} {
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
