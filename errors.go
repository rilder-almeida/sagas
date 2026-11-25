package sagas

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// KindError labels a class of error. Only this package creates one.
type KindError struct {
	name    string
	message string
}

// Error returns the message of the kind, or an empty string for a nil kind.
func (k *KindError) Error() string {
	if k == nil {
		return ""
	}

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
	// ErrGraceEnded is the cause of the recovery context when the grace period ends.
	ErrGraceEnded = &KindError{name: "grace_ended", message: "sagas: recovery grace period ended"}
	// ErrFailed labels the error of a step whose call failed.
	ErrFailed = &KindError{name: "failed", message: "sagas: step failed"}
	// ErrUnknown labels the error of a step whose outcome is unknown.
	ErrUnknown = &KindError{name: "unknown", message: "sagas: step outcome unknown"}
	// ErrPanic labels the error of a step whose call panicked.
	ErrPanic = &KindError{name: "panic", message: "sagas: panic"}
)

// Error reports a step whose action or compensation did not succeed.
type Error struct {
	Kind         *KindError
	Step         string
	Compensation bool
	Attempts     int
	Err          error
	Cause        error
	Previous     []error
}

// Error returns a message that names the step and its failure.
func (e *Error) Error() string {
	what := "step"
	if e.Compensation {
		what = "compensation of step"
	}

	msg := fmt.Sprintf("sagas: %s %q did not run", what, e.Step)
	if e.Attempts > 0 {
		msg = fmt.Sprintf("sagas: %s %q did not succeed after %d attempts", what, e.Step, e.Attempts)
	}

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

// RunError reports a run that began and did not complete.
type RunError struct {
	Kind *KindError
	// Steps holds one *Error for each action or compensation that did not succeed, in order, so one step can
	// appear twice.
	Steps []*Error
	// Pending holds, in step order, each unknown step, unless it is in the compensable zone and its compensation is
	// missing or succeeded. It also holds each step whose compensation failed, was blocked or was halted. After a
	// succeeded pivot, it holds each forward-only step that failed or did not start.
	Pending []string
	causes  []error
}

// Error returns the message of Kind followed by the texts of Unwrap.
func (e *RunError) Error() string {
	errs := e.Unwrap()
	if len(errs) == 0 {
		return e.Kind.Error()
	}

	return e.Kind.Error() + ": " + strings.Join(texts(errs), "; ")
}

// Unwrap returns the step errors, then the journal rejection, the journal errors, the cause of a caller's context
// that stopped the run and the write-back error.
func (e *RunError) Unwrap() []error {
	errs := make([]error, 0, len(e.Steps)+len(e.causes))
	for _, step := range e.Steps {
		errs = append(errs, step)
	}

	return append(errs, e.causes...)
}

// Is reports whether target is the Kind of e.
func (e *RunError) Is(target error) bool {
	kind, ok := target.(*KindError)

	return ok && kind == e.Kind
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

func kindOf(kind outcomeKind, label *KindError) *KindError {
	switch {
	case label != nil:
		return label
	case kind == outcomeUnknown:
		return ErrUnknown
	default:
		return ErrFailed
	}
}

var (
	errStepStopped      = errors.New("sagas: a step stopped the run")
	errRejectionStopped = errors.New("sagas: the journal rejected an event and stopped the run")
	errJournalStopped   = errors.New("sagas: a journal failure stopped the run")
)
