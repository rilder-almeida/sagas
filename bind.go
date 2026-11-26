package sagas

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// None is the output type of a step that produces nothing.
type None struct{}

// Step declares a step of state S and output R for a type that embeds it. Its methods must be idempotent, safe for
// concurrent use, and honor their context. Its Enabled method is a fast decision on the state. The context of Enabled
// serves trace, log and cancel, and network work belongs in a step.
type Step[S, R any] struct {
	typed[S, R]

	// Name is the name of the step. Empty means the type name in kebab case.
	Name string
	// Description is free text that Inspect reports.
	Description string
	// RetryPolicy is the retry policy of the action of a step before the pivot, bounded by the caller's context.
	RetryPolicy RetryPolicy
	// AttemptTimeout bounds each attempt of Execute before the pivot. Zero means DefaultAttemptTimeout.
	AttemptTimeout time.Duration
	// RecoveryPolicy is the retry policy of the compensation, and of the action of a step after the pivot.
	RecoveryPolicy RecoveryPolicy
	// FailurePolicy sets what the saga does when the compensation of this step fails. Empty means Block.
	FailurePolicy FailurePolicy
}

// Pivot declares the pivot step of state S and output R for a type that embeds it. Its methods follow the rules of
// Step.
type Pivot[S, R any] struct {
	typed[S, R]

	// Name is the name of the pivot. Empty means the type name in kebab case.
	Name string
	// Description is free text that Inspect reports.
	Description string
	// RetryPolicy is the retry policy of the pivot action, bounded by WithRecoveryTimeout after the caller's context
	// ends.
	RetryPolicy RetryPolicy
	// AttemptTimeout bounds each attempt of Execute. Zero means DefaultAttemptTimeout.
	AttemptTimeout time.Duration
}

type typed[S, R any] struct{}

type stepSettings struct {
	name        string
	description string
	retry       RetryPolicy
	timeout     time.Duration
	recovery    RecoveryPolicy
	failure     FailurePolicy
	pivot       bool
}

type ruleList []string

type binder interface {
	Flow
	output() reflect.Type
}

type stateBinder[S any] interface {
	bind(outer any, field int) (stepMethods[S], ruleList)
}

type encodedOutput struct {
	data []byte
	err  error
}

func (*typed[S, R]) flow() {}

func (*typed[S, R]) output() reflect.Type { return outputType[R]() }

func (*typed[S, R]) bind(outer any, field int) (stepMethods[S], ruleList) {
	return bindMethods[S, R](outer, field)
}

func outputType[R any]() reflect.Type {
	if t := reflect.TypeFor[R](); t != reflect.TypeFor[None]() {
		return t
	}

	return nil
}

type executor[S any] interface {
	Execute(ctx context.Context, s S) error
}

type producer[S, R any] interface {
	Execute(ctx context.Context, s S) (R, error)
}

type restorer[S any] interface {
	Compensate(ctx context.Context, s S) error
}

type replacer[S, R any] interface {
	Compensate(ctx context.Context, s S) (R, error)
}

type enabler[S any] interface {
	Enabled(ctx context.Context, s S) bool
}

func bindMethods[S, R any](outer any, field int) (stepMethods[S], ruleList) {
	var m stepMethods[S]

	encode := encoder[R](reflect.TypeFor[S](), field)

	rules := bindExecute(&m, outer, encode)
	rules = append(rules, bindCompensate(&m, outer, encode)...)

	if e, ok := outer.(enabler[S]); ok {
		m.enabled = e.Enabled
	} else {
		rules = append(rules, method(outer, "Enabled", reflect.TypeFor[func(context.Context, S) bool]())...)
	}

	if w, ok := outer.(Middleware); ok {
		m.wrap = w
	} else {
		rules = append(rules, method(outer, "Wrap", reflect.TypeFor[func(context.Context, CallInfo, Call) error]())...)
	}

	return m, rules
}

func bindExecute[S, R any](m *stepMethods[S], outer any, encode func(R) encodedOutput) ruleList {
	if outputType[R]() == nil {
		if e, ok := outer.(executor[S]); ok {
			m.call = func(ctx context.Context, s *S) (encodedOutput, error) {
				return encodedOutput{}, e.Execute(ctx, *s)
			}

			return nil
		}

		return method(outer, "Execute", reflect.TypeFor[func(context.Context, S) error]())
	}

	if e, ok := outer.(producer[S, R]); ok {
		m.call = encoding(e.Execute, encode)

		return nil
	}

	return method(outer, "Execute", reflect.TypeFor[func(context.Context, S) (R, error)]())
}

func bindCompensate[S, R any](m *stepMethods[S], outer any, encode func(R) encodedOutput) ruleList {
	if c, ok := outer.(restorer[S]); ok {
		m.compensate = func(ctx context.Context, s *S) (encodedOutput, error) {
			return encodedOutput{}, c.Compensate(ctx, *s)
		}

		return nil
	}

	restore := reflect.TypeFor[func(context.Context, S) error]()
	if outputType[R]() == nil {
		return method(outer, "Compensate", restore)
	}

	if c, ok := outer.(replacer[S, R]); ok {
		m.compensate, m.replaces = encoding(c.Compensate, encode), true

		return nil
	}

	return method(outer, "Compensate", restore, reflect.TypeFor[func(context.Context, S) (R, error)]())
}

func encoding[S, R any](
	call func(context.Context, S) (R, error), encode func(R) encodedOutput,
) func(context.Context, *S) (encodedOutput, error) {
	return func(ctx context.Context, s *S) (encodedOutput, error) {
		out, err := call(ctx, *s)
		if err != nil {
			return encodedOutput{}, err
		}

		return encode(out), nil
	}
}

func encoder[R any](state reflect.Type, field int) func(R) encodedOutput {
	name := reflect.TypeFor[R]().Name()
	if state.Kind() == reflect.Struct && field >= 0 && field < state.NumField() {
		name = state.Field(field).Name
	}

	return func(out R) encodedOutput {
		data, err := encodeField(name, out)

		return encodedOutput{data: data, err: err}
	}
}

func method(outer any, name string, accepted ...reflect.Type) ruleList {
	m := reflect.ValueOf(outer).MethodByName(name)
	if !m.IsValid() || slices.Contains(accepted, m.Type()) {
		return nil
	}

	want := make([]string, 0, len(accepted))
	for _, t := range accepted {
		want = append(want, t.String())
	}

	return ruleList{
		fmt.Sprintf("method %s of type %T is %s, want %s", name, outer, m.Type(), strings.Join(want, " or ")),
	}
}

func namedOutput(output reflect.Type) ruleList {
	if output != nil && (output.Name() == "" || output.PkgPath() == "") {
		return ruleList{fmt.Sprintf("output type %s: want a type declared with a name", output)}
	}

	return nil
}

func outputField(state, output reflect.Type) (int, ruleList) {
	if rules := namedOutput(output); output == nil || rules != nil || state.Kind() != reflect.Struct {
		return -1, rules
	}

	field := -1

	for i := range state.NumField() {
		if state.Field(i).Type != output {
			continue
		}

		if field >= 0 {
			return -1, ruleList{fmt.Sprintf("output type %s: fields %s and %s of state %s both have it",
				output, state.Field(field).Name, state.Field(i).Name, state)}
		}

		field = i
	}

	if field < 0 {
		return -1, ruleList{fmt.Sprintf("output type %s: no field in state %s", output, state)}
	}

	return field, nil
}

func concurrentOutputs(reports []StepReport, outputs []reflect.Type, g *graph) []error {
	var errs []error

	for i := range outputs {
		for k := i + 1; k < len(outputs); k++ {
			if outputs[i] != nil && outputs[i] == outputs[k] && g.concurrent(i, k) {
				errs = append(errs, fmt.Errorf("%w: steps %q and %q: both produce %s and may run at the same time",
					ErrInvalidStep, reports[i].Name, reports[k].Name, outputs[i]))
			}
		}
	}

	return errs
}

func idOutputs(reports []StepReport, fields []int, state reflect.Type, idField int) []error {
	var errs []error

	for i, field := range fields {
		if idField >= 0 && field == idField {
			errs = append(errs, fmt.Errorf("%w: step %q: output type %s: field %s of state %s names the execution",
				ErrInvalidStep, reports[i].Name, state.Field(field).Type, state.Field(field).Name, state))
		}
	}

	return errs
}
