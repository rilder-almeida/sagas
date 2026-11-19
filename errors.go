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
// errors.Is also walks Cause and Previous; read the kind of the step itself with errors.As and its Kind field.
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
