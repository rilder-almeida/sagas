package sagas

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// KindError labels a class of error. The package creates every KindError, and errors.Is compares the pointer.
type KindError struct {
	name    string
	message string
}

// Error returns the message of the kind.
func (k *KindError) Error() string {
	return k.message
}

// Name returns the snake_case label of the kind, as the journal stores it.
func (k *KindError) Name() string {
	if k == nil {
		return ""
	}

	return k.name
}

var (
	// ErrInvalidStep is returned by New for a step or saga option that breaks a rule of the definition.
	ErrInvalidStep = &KindError{name: "invalid_step", message: "sagas: invalid step"}
	// ErrDuplicateStep is returned by New when two steps share a name.
	ErrDuplicateStep = &KindError{name: "duplicate_step", message: "sagas: duplicate step"}
	// ErrUnknownDependency is returned by New when a step depends on a step that does not exist.
	ErrUnknownDependency = &KindError{name: "unknown_dependency", message: "sagas: unknown dependency"}
	// ErrCycle is returned by New when the steps depend on each other in a loop.
	ErrCycle = &KindError{name: "cycle", message: "sagas: cycle"}
	// ErrJournal wraps an Append error that is not a rejection; a Journal returns its own failures as is.
	ErrJournal = &KindError{name: "journal", message: "sagas: journal failure"}
	// ErrExecutionExists is returned when the journal rejects the first event because the execution already exists.
	ErrExecutionExists = &KindError{name: "execution_exists", message: "sagas: execution exists"}
	// ErrConcurrentWriter is returned when the journal rejects a later event because another writer appended first.
	ErrConcurrentWriter = &KindError{name: "concurrent_writer", message: "sagas: concurrent writer"}
	// ErrInvalidState is returned when the state type or a state value cannot round-trip through its JSON encoding.
	ErrInvalidState = &KindError{name: "invalid_state", message: "sagas: invalid state"}
	// ErrConflict is returned when a step changes a field that another step committed after the step's snapshot.
	ErrConflict = &KindError{name: "conflict", message: "sagas: state conflict"}
	// ErrCompensated is returned when a run fails and every started compensable step is compensated.
	ErrCompensated = &KindError{name: "compensated", message: "sagas: compensated"}
	// ErrCompensationFailed is returned when a run fails and at least one compensation does not succeed.
	ErrCompensationFailed = &KindError{name: "compensation_failed", message: "sagas: compensation failed"}
	// ErrIncomplete is returned when a run cannot finish forward: the pivot is unknown or forward-only work is undone.
	ErrIncomplete = &KindError{name: "incomplete", message: "sagas: incomplete"}
	// ErrInvalidRun is returned by Run for a run option or an execution ID that breaks a rule of the run input.
	ErrInvalidRun = &KindError{name: "invalid_run", message: "sagas: invalid run"}
	// ErrCallNotRun is the error of a call that middleware returned from without running the action or compensation.
	ErrCallNotRun = &KindError{name: "call_not_run", message: "sagas: middleware returned without running the call"}
	// ErrCallUnfinished is the error of a call that middleware returned from while the call was still running.
	ErrCallUnfinished = &KindError{
		name: "call_unfinished", message: "sagas: middleware returned before the call finished",
	}
	// ErrCallConcurrent is the error of a call that middleware started while another call of the attempt was running.
	ErrCallConcurrent = &KindError{
		name: "call_concurrent", message: "sagas: middleware called next while a call was running",
	}
	// ErrCallContextLost is the error of a call whose context no longer carries the call state.
	ErrCallContextLost = &KindError{
		name: "call_context_lost", message: "sagas: middleware passed on a context that lost the call state",
	}
	// ErrGraceEnded is the cause of the recovery context when the grace period ends.
	ErrGraceEnded = &KindError{name: "grace_ended", message: "sagas: recovery grace period ended"}
	// ErrFailed labels the error of a step whose call failed.
	ErrFailed = &KindError{name: "failed", message: "sagas: step failed"}
	// ErrUnknown labels the error of a step whose outcome is unknown.
	ErrUnknown = &KindError{name: "unknown", message: "sagas: step outcome unknown"}
	// ErrPanic labels the error of a step whose call panicked.
	ErrPanic = &KindError{name: "panic", message: "sagas: panic"}
)

// Error reports a step whose action or compensation did not succeed, with its kind, attempts, cause and history.
type Error struct {
	Kind         *KindError
	Step         string
	Compensation bool
	Attempts     int
	Err          error
	Cause        error
	Previous     []error
}

// Error names the step, whether its compensation failed, the attempts, the error, the cause and the history.
func (e *Error) Error() string {
	what := "step"
	if e.Compensation {
		what = "compensation of step"
	}

	msg := fmt.Sprintf("sagas: %s %q failed after %d attempts", what, e.Step, e.Attempts)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}

	if e.Cause != nil {
		msg += "; cause: " + e.Cause.Error()
	}

	if len(e.Previous) > 0 {
		msg += "; previous: " + strings.Join(texts(e.Previous), "; ")
	}

	return msg
}

// Unwrap returns Err, Cause and Previous, without the nil ones.
func (e *Error) Unwrap() []error {
	all := slices.Concat([]error{e.Err, e.Cause}, e.Previous)

	return slices.DeleteFunc(all, func(err error) bool { return err == nil })
}

// Is reports whether target is the Kind of e.
func (e *Error) Is(target error) bool {
	kind, ok := target.(*KindError)

	return ok && kind == e.Kind
}

// LogValue gives every field of e to slog.
func (e *Error) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("kind", e.Kind.Name()),
		slog.String("step", e.Step),
		slog.Bool("compensation", e.Compensation),
		slog.Int("attempts", e.Attempts),
	}

	if e.Err != nil {
		attrs = append(attrs, slog.String("err", e.Err.Error()))
	}

	if e.Cause != nil {
		attrs = append(attrs, slog.String("cause", e.Cause.Error()))
	}

	if len(e.Previous) > 0 {
		attrs = append(attrs, slog.Any("previous", texts(e.Previous)))
	}

	return slog.GroupValue(attrs...)
}

func text(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func texts(errs []error) []string {
	if len(errs) == 0 {
		return nil
	}

	out := make([]string, len(errs))
	for i, err := range errs {
		out[i] = text(err)
	}

	return out
}

func eventErrorOf(e *Error) EventError {
	return EventError{Kind: e.Kind.Name(), Message: text(e.Err), Cause: text(e.Cause), Previous: texts(e.Previous)}
}

func kindOf(kind outcomeKind, err error) *KindError {
	if _, ok := errors.AsType[*PanicError](err); ok {
		return ErrPanic
	}

	packageKinds := []*KindError{
		ErrConflict,
		ErrInvalidState,
		ErrJournal,
		ErrCallNotRun,
		ErrCallUnfinished,
		ErrCallConcurrent,
		ErrCallContextLost,
	}

	for _, k := range packageKinds {
		if errors.Is(err, k) {
			return k
		}
	}

	if kind == outcomeUnknown {
		return ErrUnknown
	}

	return ErrFailed
}

type callError struct {
	err      error
	cause    error
	previous []error
}

func (e *callError) Error() string { return e.err.Error() }

func (e *callError) Unwrap() error { return e.err }

func asCallError(err error) *callError {
	if ce, ok := errors.AsType[*callError](err); ok {
		clone := *ce

		return &clone
	}

	return &callError{err: err}
}

func withCause(err, cause error) error {
	ce := asCallError(err)
	if ce.cause == nil {
		ce.cause = cause
	}

	return ce
}

func withPrevious(err error, prior []error) error {
	if len(prior) == 0 {
		return err
	}

	ce := asCallError(err)
	ce.previous = slices.Concat(prior, ce.previous)

	return ce
}

func replaced(err, original error) error {
	if original == nil || errors.Is(err, original) {
		return err
	}

	return &callError{err: err, previous: []error{original}}
}

func record(ce *callError) []error {
	last := ce.err
	if ce.cause != nil {
		last = fmt.Errorf("%w: %w", ce.err, ce.cause)
	}

	return append(slices.Clone(ce.previous), last)
}
