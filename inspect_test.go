package sagas

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Reserve struct{ Step }

func (Reserve) Execute(context.Context, *tripState) error { return nil }

type CheckFraud struct{ Step }

func (*CheckFraud) Execute(context.Context, *tripState) error { return nil }

type SendHTTPRequest struct{ Step }

func (SendHTTPRequest) Execute(context.Context, *tripState) error { return nil }

type Notify[T any] struct {
	Step

	Value T
}

func (Notify[T]) Execute(context.Context, *tripState) error { return nil }

type _lead struct{ Step }

func (_lead) Execute(context.Context, *tripState) error { return nil }

type typoStep struct{ Step }

func (typoStep) Execute(context.Context, *tripState) error { return nil }

func (typoStep) Compensat(context.Context, *tripState) error { return nil }

type idleStep struct{ Step }

type bothStep struct {
	Step
	doer
	payer
}

type wrongStep struct{ Step }

func (wrongStep) Execute(context.Context, tripState) error { return nil }

type wrongEnabledStep struct {
	Step
	doer
}

func (wrongEnabledStep) Enabled(*tripState) error { return nil }

type otherWrapper struct{}

func (otherWrapper) Wrap(ctx context.Context, _ CallInfo, call Call) error { return call(ctx) }

type twoWraps struct {
	Step
	doer
	wrapper
	otherWrapper
}

type otherDoer struct{}

func (otherDoer) Execute(context.Context, *tripState) error { return nil }

type otherUndoer struct{}

func (otherUndoer) Compensate(context.Context, *tripState) error { return nil }

type otherChecker struct{}

func (otherChecker) Enabled(*tripState) bool { return true }

type twoDoers struct {
	Step
	doer
	otherDoer
}

type twoUndoers struct {
	Step
	doer
	undoer
	otherUndoer
}

type twoCheckers struct {
	Step
	doer
	checker
	otherChecker
}

type ifaceWrap struct {
	Step
	doer
	wrapper
	Middleware
}

type sharedA struct {
	*Step
	doer
}

type sharedB struct {
	*Step
	doer
}

type nestedReserve struct{ Reserve }

type ptrReserve struct{ *Reserve }

type pairWraps struct {
	wrapper
	otherWrapper
}

type nestedWraps struct {
	Step
	doer
	pairWraps
}

type pairDoers struct {
	doer
	otherDoer
}

type nestedDoers struct {
	Step
	pairDoers
}

type deepWrap struct{ wrapper }

type mixedDepth struct {
	Step
	doer
	wrapper
	otherWrapper
	deepWrap
}

type cycleA struct {
	Step
	doer
	*cycleB
}

type cycleB struct{ *cycleA }

type ownWrap struct {
	Step
	doer
	wrapper
	otherWrapper
}

func (ownWrap) Wrap(ctx context.Context, _ CallInfo, call Call) error { return call(ctx) }

type okStep[S any] struct{ Step }

func (okStep[S]) Execute(context.Context, *S) error { return nil }

type goodIDState struct{ ID string }

func (s goodIDState) ExecutionID() string { return s.ID }

type badIDState struct{ ID string }

func (*badIDState) ExecutionID() int { return 0 }

const (
	setName       = "set Name"
	cycleText     = "form a cycle"
	reserveName   = "reserve"
	stepA         = `step "a"`
	quotedA       = `"a"`
	negAttempts   = "RetryPolicy.Attempts is negative"
	badName       = "name may hold only ASCII letters"
	neitherText   = "neither Execute nor Pivot"
	fullName      = "full"
	executeMethod = "Execute"
	wrapMethod    = "Wrap"
)

type inspectRow struct {
	name         string
	inspect      func(Flow) ([]StepReport, error)
	flow         Flow
	wantReports  []StepReport
	wantErrs     []error
	wantNoErrs   []error
	wantContains []string
	wantAbsent   []string
}

func inspectSuccessRows() []inspectRow {
	trip := Inspect[tripState]
	full := &fullStep{
		Name: fullName, Description: "books the room", RetryPolicy: NoRetry(), AttemptTimeout: time.Second,
		FailurePolicy: Halt,
		do:            okAction, undo: okAction, pred: okPredicate, wrap: okMiddleware(),
	}

	return []inspectRow{
		{
			name:    "[Success] When: a step sets every field and has every method ; Should: report all of them",
			inspect: trip, flow: full,
			wantReports: []StepReport{{
				Name: fullName, Description: "books the room", RetryPolicy: NoRetry(), AttemptTimeout: time.Second,
				FailurePolicy: Halt, Execute: true, Compensate: true, Enabled: true, Wrap: true,
			}},
		},
		{
			name:    "[Success] When: the type declares Execute on the value receiver ; Should: find it",
			inspect: trip, flow: &Reserve{},
			wantReports: []StepReport{{Name: reserveName, Execute: true}},
		},
		{
			name:    "[Success] When: the type declares Execute on the pointer receiver ; Should: find it",
			inspect: trip, flow: &CheckFraud{},
			wantReports: []StepReport{{Name: "check-fraud", Execute: true}},
		},
		{
			name:    "[Success] When: the type name holds an acronym ; Should: keep the acronym as one word",
			inspect: trip, flow: &SendHTTPRequest{},
			wantReports: []StepReport{{Name: "send-http-request", Execute: true}},
		},
		{
			name:    "[Success] When: the type is generic ; Should: drop the type arguments from the name",
			inspect: trip, flow: &Notify[int]{},
			wantReports: []StepReport{{Name: "notify", Execute: true}},
		},
		{
			name:    "[Success] When: Name is set ; Should: use it over the type name",
			inspect: trip, flow: &Reserve{Name: bookStep},
			wantReports: []StepReport{{Name: bookStep, Execute: true}},
		},
		{
			name:    "[Success] When: two steps of one type each set Name ; Should: report both",
			inspect: trip, flow: Sequence(&Reserve{Name: "x"}, &Reserve{Name: "y"}),
			wantReports: []StepReport{{Name: "x", Execute: true}, {Name: "y", Execute: true, After: []string{"x"}}},
		},
		{
			name:    "[Success] When: a method is misspelled Compensat ; Should: report Compensate false",
			inspect: trip, flow: &typoStep{},
			wantReports: []StepReport{{Name: "typo-step", Execute: true}},
		},
		{
			name:    "[Success] When: the type declares Wrap beside two embedded ones ; Should: report Wrap",
			inspect: trip, flow: &ownWrap{do: okAction, wrap: okMiddleware(), otherWrapper: otherWrapper{}},
			wantReports: []StepReport{{Name: "own-wrap", Execute: true, Wrap: true}},
		},
		{
			name:    "[Success] When: a pivot sits between a compensable step and a forward step ; Should: report it",
			inspect: trip,
			flow:    Sequence(undoing("a", okAction, okAction), paying("p", okAction), act("z", okAction)),
			wantReports: []StepReport{
				{Name: "a", Execute: true, Compensate: true},
				{Name: "p", Pivot: true, After: []string{"a"}},
				{Name: "z", Execute: true, After: []string{"p"}},
			},
		},
		{
			name:        "[Success] When: two types embed each other by pointer ; Should: end the walk and report",
			inspect:     trip,
			flow:        &cycleA{do: okAction, cycleB: &cycleB{}},
			wantReports: []StepReport{{Name: "cycle-a", Execute: true}},
		},
		{
			name:        "[Success] When: the type embeds Step through a struct by value ; Should: find its methods",
			inspect:     trip,
			flow:        &nestedReserve{},
			wantReports: []StepReport{{Name: "nested-reserve", Execute: true}},
		},
		{
			name:    "[Success] When: the state declares ExecutionID() string ; Should: accept it",
			inspect: Inspect[goodIDState], flow: &okStep[goodIDState]{},
			wantReports: []StepReport{{Name: "ok-step", Execute: true}},
		},
	}
}

func inspectStepRows() []inspectRow {
	trip := Inspect[tripState]
	compensable := act("a", okAction)
	compensable.FailurePolicy = Halt
	timed := act("a", okAction)
	timed.AttemptTimeout = -time.Second
	retried := act("a", okAction)
	retried.RetryPolicy = RetryPolicy{Attempts: -1}
	unknown := undoing("a", okAction, okAction)
	unknown.FailurePolicy = "stop"

	var (
		typedNil *Reserve
		nilGroup *Group
	)

	return []inspectRow{
		{
			name:    "[Fail] When: the type has neither Execute nor Pivot ; Should: name the step and the type",
			inspect: trip, flow: &idleStep{},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "idle-step"`, "*sagas.idleStep", neitherText},
		},
		{
			name:    "[Fail] When: the type has both Execute and Pivot ; Should: name the step and the type",
			inspect: trip, flow: &bothStep{do: okAction, pay: okAction},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "both-step"`, "*sagas.bothStep", "both Execute and Pivot"},
		},
		{
			name:    "[Fail] When: Execute has the wrong signature ; Should: name the step and the method",
			inspect: trip, flow: &wrongStep{},
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				`step "wrong-step"`, "method Execute", "func(context.Context, sagas.tripState) error",
				"want func(context.Context, *sagas.tripState) error",
			},
		},
		{
			name:    "[Fail] When: Enabled has the wrong signature ; Should: name the step and the method",
			inspect: trip, flow: &wrongEnabledStep{do: okAction},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "wrong-enabled-step"`, "method Enabled", "want func(*sagas.tripState) bool"},
		},
		{
			name:    "[Fail] When: the step is for another state type ; Should: name the method and the state",
			inspect: Inspect[otherState], flow: act("a", okAction),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{stepA, "method Execute", "want func(context.Context, *sagas.otherState) error"},
		},
		{
			name:    "[Fail] When: FailurePolicy is set without Compensate ; Should: reject it",
			inspect: trip, flow: compensable,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{stepA, "FailurePolicy without Compensate"},
		},
		{
			name:    "[Fail] When: FailurePolicy is unknown ; Should: name the policy",
			inspect: trip, flow: unknown,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{stepA, `"stop"`},
		},
		{
			name:    "[Fail] When: AttemptTimeout is negative ; Should: reject it",
			inspect: trip, flow: timed,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{stepA, "AttemptTimeout is negative"},
		},
		{
			name:    "[Fail] When: RetryPolicy has a negative field ; Should: name the field",
			inspect: trip, flow: retried,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{stepA, negAttempts},
		},
		{
			name:    "[Fail] When: an explicit name breaks the name rule ; Should: name the step",
			inspect: trip, flow: act("a b", okAction),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "a b"`, badName},
			wantAbsent:   []string{setName},
		},
		{
			name:    "[Fail] When: the type name breaks the name rule ; Should: name the step and say to set Name",
			inspect: trip, flow: &_lead{},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "_lead"`, badName, setName},
		},
		{
			name:    "[Fail] When: two steps set one name ; Should: return ErrDuplicateStep naming it",
			inspect: trip, flow: Sequence(act("a", okAction), act("a", okAction)),
			wantErrs:     []error{ErrDuplicateStep},
			wantContains: []string{`step "a": declared more than once`},
			wantAbsent:   []string{setName},
		},
		{
			name:    "[Fail] When: two steps of one type have no Name ; Should: say to set Name on each one",
			inspect: trip, flow: Parallel(&Reserve{}, &Reserve{}),
			wantErrs: []error{ErrDuplicateStep},
			wantContains: []string{
				`step "reserve": declared more than once`, "set Name on each step of type *sagas.Reserve",
			},
		},
		{
			name:    "[Fail] When: a Sequence item is nil ; Should: reject it",
			inspect: trip, flow: Sequence(act("a", okAction), nil),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"Sequence item at index 1 is nil"},
		},
		{
			name:         "[Fail] When: a Sequence item is a nil Group ; Should: reject it without a panic",
			inspect:      trip,
			flow:         Sequence(act("a", okAction), nilGroup),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"Sequence item at index 1 is nil"},
		},
		{
			name:    "[Fail] When: a Parallel item is a typed nil pointer ; Should: reject it without a panic",
			inspect: trip, flow: Parallel(act("a", okAction), typedNil),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"Parallel item at index 1 is nil"},
		},
		{
			name:         "[Fail] When: the flow is nil ; Should: report a saga without steps",
			inspect:      trip,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{noStepsText},
		},
		{
			name:    "[Fail] When: the flow holds only an empty group ; Should: name the group and report no steps",
			inspect: trip, flow: Sequence(Parallel()),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"Sequence item at index 0 is an empty Parallel", noStepsText},
		},
		{
			name:    "[Fail] When: the flow is an empty Sequence ; Should: report a saga without steps only",
			inspect: trip, flow: Sequence(),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{noStepsText},
			wantAbsent:   []string{"is an empty"},
		},
		{
			name:    "[Fail] When: a Sequence item is an empty Parallel ; Should: reject it naming its position",
			inspect: trip, flow: Sequence(act("a", okAction), Parallel()),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"Sequence item at index 1 is an empty Parallel"},
			wantAbsent:   []string{noStepsText},
		},
		{
			name:    "[Fail] When: a Parallel item is an empty Sequence ; Should: reject it naming its position",
			inspect: trip, flow: Parallel(Sequence(), act("a", okAction)),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"Parallel item at index 0 is an empty Sequence"},
			wantAbsent:   []string{noStepsText},
		},
		{
			name:    "[Fail] When: the state type breaks the state rules ; Should: return ErrInvalidState",
			inspect: Inspect[chanState], flow: &okStep[chanState]{},
			wantErrs:     []error{ErrInvalidState},
			wantNoErrs:   []error{ErrInvalidStep},
			wantContains: []string{"chanState"},
		},
		{
			name:    "[Fail] When: a step and the state type are both bad ; Should: join both errors",
			inspect: Inspect[chanState], flow: &idleStep{},
			wantErrs:     []error{ErrInvalidStep, ErrInvalidState},
			wantContains: []string{neitherText, "chanState"},
		},
		{
			name:         "[Fail] When: the state ExecutionID has another signature ; Should: return ErrInvalidState",
			inspect:      Inspect[badIDState],
			flow:         &okStep[badIDState]{},
			wantErrs:     []error{ErrInvalidState},
			wantNoErrs:   []error{ErrInvalidStep},
			wantContains: []string{"badIDState", "method ExecutionID", "func() int", "want func() string"},
		},
	}
}

func inspectEmbedRows() []inspectRow {
	trip := Inspect[tripState]
	shared := &Step{Name: "shared"}

	return []inspectRow{
		{
			name:    "[Fail] When: the type embeds two fields with Wrap and declares none ; Should: name the fields",
			inspect: trip, flow: &twoWraps{do: okAction, wrap: okMiddleware(), otherWrapper: otherWrapper{}},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "two-wraps"`, "declares no Wrap and embeds one in wrapper and otherWrapper"},
		},
		{
			name:     "[Fail] When: the type embeds a struct and an interface with Wrap ; Should: name both fields",
			inspect:  trip,
			flow:     &ifaceWrap{do: okAction, wrap: okMiddleware(), Middleware: okMiddleware()},
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				`step "iface-wrap"`, "declares no Wrap and embeds one in wrapper and Middleware",
			},
		},
		{
			name:     "[Fail] When: a nested struct promotes two Wrap ; Should: name both field paths",
			inspect:  trip,
			flow:     &nestedWraps{do: okAction, wrap: okMiddleware(), otherWrapper: otherWrapper{}},
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				`step "nested-wraps"`,
				"declares no Wrap and embeds one in pairWraps.wrapper and pairWraps.otherWrapper",
			},
		},
		{
			name:     "[Fail] When: a nested struct promotes two Execute ; Should: name both field paths only",
			inspect:  trip,
			flow:     &nestedDoers{do: okAction, otherDoer: otherDoer{}},
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				`step "nested-doers"`, "declares no Execute and embeds one in pairDoers.doer and pairDoers.otherDoer",
			},
			wantAbsent: []string{neitherText},
		},
		{
			name:    "[Fail] When: two Wrap sit at depth one and one deeper ; Should: name only the depth one fields",
			inspect: trip,
			flow: &mixedDepth{
				do:           okAction,
				wrap:         okMiddleware(),
				otherWrapper: otherWrapper{},
				deepWrap:     deepWrap{},
			},
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				`step "mixed-depth"`, "declares no Wrap and embeds one in wrapper and otherWrapper",
			},
			wantAbsent: []string{"deepWrap"},
		},
		{
			name:     "[Fail] When: the type reaches Step through a pointer ; Should: reject it naming the type",
			inspect:  trip,
			flow:     Sequence(&ptrReserve{Reserve: &Reserve{}}),
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				"Sequence item at index 0 has type *sagas.ptrReserve, want a pointer to a struct that embeds Step",
			},
		},
		{
			name:     "[Fail] When: two embedded fields promote Execute ; Should: name the method and both fields",
			inspect:  trip,
			flow:     &twoDoers{do: okAction, otherDoer: otherDoer{}},
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				`step "two-doers"`, "declares no Execute and embeds one in doer and otherDoer",
			},
			wantAbsent: []string{neitherText},
		},
		{
			name:     "[Fail] When: two embedded fields promote Compensate ; Should: name the method and both fields",
			inspect:  trip,
			flow:     &twoUndoers{do: okAction, undo: okAction, otherUndoer: otherUndoer{}},
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				`step "two-undoers"`, "declares no Compensate and embeds one in undoer and otherUndoer",
			},
		},
		{
			name:     "[Fail] When: two embedded fields promote Enabled ; Should: name the method and both fields",
			inspect:  trip,
			flow:     &twoCheckers{do: okAction, pred: okPredicate, otherChecker: otherChecker{}},
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				`step "two-checkers"`, "declares no Enabled and embeds one in checker and otherChecker",
			},
		},
		{
			name:     "[Fail] When: two types embed one shared *Step ; Should: reject both items naming type and place",
			inspect:  trip,
			flow:     Sequence(&sharedA{Step: shared, do: okAction}, &sharedB{Step: shared, do: okAction}),
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				"Sequence item at index 0 has type *sagas.sharedA", "Sequence item at index 1 has type *sagas.sharedB",
				"want a pointer to a struct that embeds Step",
			},
			wantNoErrs: []error{ErrCycle},
		},
		{
			name:     "[Fail] When: a struct value is an item ; Should: reject it naming type and place",
			inspect:  trip,
			flow:     Parallel(act("a", okAction), sharedA{Step: &Step{}, do: okAction}),
			wantErrs: []error{ErrInvalidStep},
			wantContains: []string{
				"Parallel item at index 1 has type sagas.sharedA", "want a pointer to a struct that embeds Step",
			},
		},
	}
}

func inspectGraphRows() []inspectRow {
	trip := Inspect[tripState]
	s := okSteps("a", "b", "c")
	a, b, c := s[0], s[1], s[2]
	p, q := paying("p", okAction), paying("q", okAction)
	undoAfter := undoing("b", okAction, okAction)
	pivotUndo := &pivotUndoStep{Name: "p", pay: okAction, undo: okAction}
	pivotIf := &pivotIfStep{Name: "p", pay: okAction, pred: okPredicate}
	loop := Parallel(Sequence(a, b), Sequence(b, a))

	return []inspectRow{
		{
			name:    "[Fail] When: two flows order one pair both ways ; Should: return ErrCycle naming both",
			inspect: trip, flow: loop,
			wantErrs:     []error{ErrCycle},
			wantContains: []string{quotedA, `"b"`, cycleText},
		},
		{
			name:    "[Fail] When: a step waits for itself ; Should: return ErrCycle naming it",
			inspect: trip, flow: Sequence(a, a),
			wantErrs:     []error{ErrCycle},
			wantContains: []string{quotedA, cycleText},
		},
		{
			name:    "[Fail] When: a step only waits behind a cycle ; Should: name the cycle steps and not that step",
			inspect: trip, flow: Parallel(loop, Sequence(a, c)),
			wantErrs:     []error{ErrCycle},
			wantContains: []string{quotedA, `"b"`},
			wantAbsent:   []string{`"c"`},
		},
		{
			name:    "[Fail] When: two steps are pivots ; Should: reject it naming both",
			inspect: trip, flow: Sequence(p, q),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"more than one Pivot", `"p"`, `"q"`},
		},
		{
			name:         "[Fail] When: a cycle and two pivots exist ; Should: report the cycle and the count",
			inspect:      trip,
			flow:         Parallel(loop, p, q),
			wantErrs:     []error{ErrCycle, ErrInvalidStep},
			wantContains: []string{"more than one Pivot"},
			wantAbsent:   []string{noReach},
		},
		{
			name:    "[Fail] When: two pivots and a step parallel to both exist ; Should: report only the pivot count",
			inspect: trip, flow: Parallel(Sequence(p, q), c),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"more than one Pivot"},
			wantAbsent:   []string{noReach},
		},
		{
			name:    "[Fail] When: a step is parallel to the pivot ; Should: reject it naming the step",
			inspect: trip, flow: Parallel(p, b),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "b"`, "neither before nor after the pivot"},
		},
		{
			name:    "[Fail] When: the pivot has Compensate ; Should: reject it",
			inspect: trip, flow: pivotUndo,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "p"`, "pivot has Compensate"},
		},
		{
			name:    "[Fail] When: the pivot has Enabled ; Should: reject it",
			inspect: trip, flow: pivotIf,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "p"`, "pivot has Enabled"},
		},
		{
			name:    "[Fail] When: a step after the pivot has Compensate ; Should: reject it naming the step",
			inspect: trip, flow: Sequence(p, undoAfter),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "b"`, "after the pivot has Compensate"},
		},
		{
			name:    "[Fail] When: a cycle and one pivot exist ; Should: report only the cycle",
			inspect: trip, flow: Parallel(loop, p),
			wantErrs:   []error{ErrCycle},
			wantNoErrs: []error{ErrInvalidStep},
			wantAbsent: []string{noReach},
		},
		{
			name:    "[Fail] When: a cycle exists and the pivot has Compensate ; Should: report both errors",
			inspect: trip, flow: Parallel(loop, pivotUndo),
			wantErrs:     []error{ErrCycle, ErrInvalidStep},
			wantContains: []string{"pivot has Compensate"},
		},
	}
}

const noStepsText = "saga has no steps"

func Test_Inspect(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(inspectSuccessRows(), inspectStepRows(), inspectEmbedRows(), inspectGraphRows()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.inspect(tt.flow)
			assert.Equal(t, tt.wantReports, got)
			requireErrs(t, err, tt.wantErrs, nil)

			for _, no := range tt.wantNoErrs {
				require.NotErrorIs(t, err, no)
			}

			requireText(t, err, tt.wantContains, tt.wantAbsent)
		})
	}
}

func Test_nameOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		typeName string
		want     string
	}{
		{name: "[Success] When: the type is one word ; Should: lower it", typeName: "Reserve", want: reserveName},
		{
			name:     "[Success] When: the type is two words ; Should: join them with a dash",
			typeName: "CheckFraud", want: "check-fraud",
		},
		{
			name:     "[Success] When: the type holds an acronym ; Should: keep the acronym as one word",
			typeName: "SendHTTPRequest", want: "send-http-request",
		},
		{
			name:     "[Success] When: the type ends in an acronym ; Should: keep it as the last word",
			typeName: "CallAPI", want: "call-api",
		},
		{
			name:     "[Success] When: the type is generic ; Should: drop the type arguments",
			typeName: "Notify[map[string]int]", want: "notify",
		},
		{
			name:     "[Success] When: the type is unexported ; Should: split it the same way",
			typeName: "typoStep", want: "typo-step",
		},
		{
			name:     "[Success] When: the type starts with an underscore ; Should: keep the underscore",
			typeName: "_lead", want: "_lead",
		},
		{
			name:     "[Success] When: the type has no name ; Should: return an empty name",
			typeName: "", want: "",
		},
		{
			name:     "[Success] When: the type holds a letter outside ASCII ; Should: keep the letter",
			typeName: "Café", want: "café",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, nameOf(tt.typeName))
		})
	}
}

func Test_examine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		flow           Flow
		wantNames      []string
		wantCompensate []bool
		wantOrder      []int
		wantPivot      int
		wantErrs       []error
	}{
		{
			name:           "[Success] When: a pivot sits between two steps ; Should: return methods, order and pivot",
			flow:           Sequence(undoing("a", okAction, okAction), paying("p", okAction), act("z", okAction)),
			wantNames:      []string{"a", "p", "z"},
			wantCompensate: []bool{true, false, false},
			wantOrder:      []int{0, 1, 2},
			wantPivot:      1,
		},
		{
			name:           "[Success] When: two steps run in parallel ; Should: return both with no pivot",
			flow:           Parallel(act("a", okAction), act("b", okAction)),
			wantNames:      []string{"a", "b"},
			wantCompensate: []bool{false, false},
			wantOrder:      []int{0, 1},
			wantPivot:      -1,
		},
		{
			name:           "[Fail] When: a step has no action ; Should: return its report beside the error",
			flow:           &idleStep{},
			wantNames:      []string{"idle-step"},
			wantCompensate: []bool{false},
			wantOrder:      []int{0},
			wantPivot:      -1,
			wantErrs:       []error{ErrInvalidStep},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in, err := examine[tripState](tt.flow)

			names := make([]string, 0, len(in.reports))
			for i := range in.reports {
				names = append(names, in.reports[i].Name)
			}

			compensate := make([]bool, 0, len(in.methods))
			for i := range in.methods {
				compensate = append(compensate, in.methods[i].compensate != nil)
			}

			assert.Equal(t, tt.wantNames, names)
			assert.Equal(t, tt.wantCompensate, compensate)
			assert.Equal(t, tt.wantOrder, in.graph.order)
			assert.Equal(t, tt.wantPivot, in.graph.pivot)
			requireErrs(t, err, tt.wantErrs, nil)
		})
	}
}

func Test_nameStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		flow        Flow
		wantName    string
		wantDerived bool
	}{
		{
			name: "[Success] When: Name is empty ; Should: derive the name from the type", flow: &CheckFraud{},
			wantName: "check-fraud", wantDerived: true,
		},
		{
			name: "[Success] When: the type is generic ; Should: derive the name without type arguments",
			flow: &Notify[int]{}, wantName: "notify", wantDerived: true,
		},
		{
			name: "[Success] When: Name is set ; Should: use it and not mark it derived",
			flow: &Reserve{Name: bookStep}, wantName: bookStep, wantDerived: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := nameStep(tt.flow)

			assert.Equal(t, tt.wantName, got.name)
			assert.Equal(t, tt.wantDerived, got.derived)
			assert.Equal(t, reflect.TypeOf(tt.flow), got.typ)
		})
	}
}

func Test_inspectStep(t *testing.T) {
	t.Parallel()

	full := &fullStep{
		Name: fullName, do: okAction, undo: okAction, pred: okPredicate, wrap: okMiddleware(),
		FailurePolicy: Halt,
	}

	tests := []struct {
		name           string
		flow           Flow
		wantReport     StepReport
		wantCall       bool
		wantCompensate bool
		wantEnabled    bool
		wantWrap       bool
		wantErrs       []string
	}{
		{
			name: "[Success] When: a step has every method ; Should: report and return all of them",
			flow: full,
			wantReport: StepReport{
				Name: fullName, FailurePolicy: Halt,
				Execute: true, Compensate: true, Enabled: true, Wrap: true,
			},
			wantCall: true, wantCompensate: true, wantEnabled: true, wantWrap: true,
		},
		{
			name:       "[Success] When: a step has Pivot ; Should: return Pivot as the call",
			flow:       paying("p", okAction),
			wantReport: StepReport{Name: "p", Pivot: true},
			wantCall:   true,
		},
		{
			name:       "[Fail] When: a step has no action ; Should: return the rule and no call",
			flow:       &idleStep{},
			wantReport: StepReport{Name: "idle-step"},
			wantErrs:   []string{neitherText},
		},
		{
			name:       "[Fail] When: a derived name breaks the name rule ; Should: say to set Name",
			flow:       &_lead{},
			wantReport: StepReport{Name: "_lead", Execute: true},
			wantCall:   true,
			wantErrs:   []string{nameRule + "; " + setName},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report, m, errs := inspectStep[tripState](tt.flow, nameStep(tt.flow))

			assert.Equal(t, tt.wantReport, report)
			assert.Equal(t, tt.wantCall, m.call != nil)
			assert.Equal(t, tt.wantCompensate, m.compensate != nil)
			assert.Equal(t, tt.wantEnabled, m.enabled != nil)
			assert.Equal(t, tt.wantWrap, m.wrap != nil)
			require.Len(t, errs, len(tt.wantErrs))

			for i, want := range tt.wantErrs {
				require.ErrorIs(t, errs[i], ErrInvalidStep)
				assert.Contains(t, errs[i].Error(), want)
			}
		})
	}
}

func Test_kindRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		flow      Flow
		ambiguous map[string]bool
		want      []string
	}{
		{
			name: "[Success] When: the type has Execute ; Should: return no rule",
			flow: &Reserve{}, ambiguous: map[string]bool{}, want: nil,
		},
		{
			name: "[Success] When: Execute is ambiguous ; Should: count it as present",
			flow: &twoDoers{}, ambiguous: map[string]bool{executeMethod: true}, want: nil,
		},
		{
			name: "[Fail] When: Execute is ambiguous and not marked ; Should: report neither",
			flow: &twoDoers{}, ambiguous: map[string]bool{},
			want: []string{"type *sagas.twoDoers has neither Execute nor Pivot"},
		},
		{
			name: "[Fail] When: the type has neither ; Should: report neither",
			flow: &idleStep{}, ambiguous: map[string]bool{},
			want: []string{"type *sagas.idleStep has neither Execute nor Pivot"},
		},
		{
			name: "[Fail] When: the type has both ; Should: report both",
			flow: &bothStep{}, ambiguous: map[string]bool{},
			want: []string{"type *sagas.bothStep has both Execute and Pivot"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, kindRules(reflect.ValueOf(tt.flow), &ambiguity{names: tt.ambiguous}))
		})
	}
}

func Test_method(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		flow      Flow
		method    string
		wantFound bool
		wantRules []string
	}{
		{
			name: "[Success] When: the method has the wanted signature ; Should: find it and set it",
			flow: &Reserve{}, method: executeMethod, wantFound: true,
		},
		{
			name: "[Success] When: the method is missing ; Should: find nothing and add no rule",
			flow: &idleStep{}, method: executeMethod, wantFound: false,
		},
		{
			name: "[Fail] When: the method has another signature ; Should: add a rule naming both signatures",
			flow: &wrongStep{}, method: executeMethod, wantFound: false,
			wantRules: []string{
				"method Execute of type *sagas.wrongStep is func(context.Context, sagas.tripState) error, " +
					"want func(context.Context, *sagas.tripState) error",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var (
				dst   func(context.Context, *tripState) error
				rules []string
			)

			found := method(reflect.ValueOf(tt.flow), tt.method, &dst, &rules)

			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.wantFound, dst != nil)
			assert.Equal(t, tt.wantRules, rules)
		})
	}
}

func Test_ambiguousMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		typ       reflect.Type
		wantNames map[string]bool
		wantRules []string
	}{
		{
			name: "[Success] When: every method is declared or promoted once ; Should: find nothing",
			typ:  reflect.TypeFor[*fullStep](), wantNames: map[string]bool{},
		},
		{
			name: "[Success] When: the type declares Wrap beside two embedded ones ; Should: find nothing",
			typ:  reflect.TypeFor[*ownWrap](), wantNames: map[string]bool{},
		},
		{
			name: "[Fail] When: two embedded fields give Wrap ; Should: name Wrap and both fields",
			typ:  reflect.TypeFor[*twoWraps](), wantNames: map[string]bool{wrapMethod: true},
			wantRules: []string{"type *sagas.twoWraps declares no Wrap and embeds one in wrapper and otherWrapper"},
		},
		{
			name: "[Fail] When: two fields one level down give Execute ; Should: name the field paths",
			typ:  reflect.TypeFor[*nestedDoers](), wantNames: map[string]bool{executeMethod: true},
			wantRules: []string{
				"type *sagas.nestedDoers declares no Execute and embeds one in pairDoers.doer and pairDoers.otherDoer",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ambiguousMethods(tt.typ)

			assert.Equal(t, tt.wantNames, got.names)
			assert.Equal(t, tt.wantRules, got.rules)
		})
	}
}

func Test_promoters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		typ    reflect.Type
		method string
		want   []string
	}{
		{
			name: "[Success] When: no embedded field gives the method ; Should: return nothing",
			typ:  reflect.TypeFor[Reserve](), method: executeMethod, want: nil,
		},
		{
			name: "[Success] When: one embedded field gives the method ; Should: return its path",
			typ:  reflect.TypeFor[actStep](), method: executeMethod, want: []string{"doer"},
		},
		{
			name: "[Success] When: two fields give it at the shallowest level ; Should: return both, not deeper ones",
			typ:  reflect.TypeFor[mixedDepth](), method: wrapMethod, want: []string{"wrapper", "otherWrapper"},
		},
		{
			name: "[Success] When: two fields give it one level down ; Should: return both paths",
			typ:  reflect.TypeFor[nestedDoers](), method: executeMethod,
			want: []string{"pairDoers.doer", "pairDoers.otherDoer"},
		},
		{
			name: "[Success] When: two types embed each other ; Should: end the walk and return nothing",
			typ:  reflect.TypeFor[cycleA](), method: wrapMethod, want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, promoters(tt.typ, tt.method))
		})
	}
}

func Test_scanLevel(t *testing.T) {
	t.Parallel()

	nodeOf := func(t reflect.Type, path string, ancestors ...reflect.Type) embedNode {
		return embedNode{t: t, path: path, ancestors: append(ancestors, t)}
	}

	tests := []struct {
		name          string
		level         []embedNode
		method        string
		wantProviders []string
		wantNext      []string
	}{
		{
			name:          "[Success] When: two fields give the method ; Should: return them and queue the others",
			level:         []embedNode{nodeOf(reflect.TypeFor[twoWraps](), "")},
			method:        wrapMethod,
			wantProviders: []string{"wrapper", "otherWrapper"},
			wantNext:      []string{"Step", "doer"},
		},
		{
			name:     "[Success] When: no field gives the method ; Should: queue every embedded struct",
			level:    []embedNode{nodeOf(reflect.TypeFor[nestedDoers](), "")},
			method:   executeMethod,
			wantNext: []string{"Step", "pairDoers"},
		},
		{
			name:     "[Success] When: a field leads back to an ancestor ; Should: not queue it",
			level:    []embedNode{nodeOf(reflect.TypeFor[cycleB](), "cycleB", reflect.TypeFor[cycleA]())},
			method:   wrapMethod,
			wantNext: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			providers, next := scanLevel(tt.level, tt.method)

			var paths []string
			for _, n := range next {
				paths = append(paths, n.path)
			}

			assert.Equal(t, tt.wantProviders, providers)
			assert.Equal(t, tt.wantNext, paths)
		})
	}
}

func Test_provides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		typ    reflect.Type
		method string
		want   bool
	}{
		{
			name: "[Success] When: the type declares the method ; Should: provide it",
			typ:  reflect.TypeFor[wrapper](), method: wrapMethod, want: true,
		},
		{
			name: "[Success] When: an interface has the method ; Should: provide it",
			typ:  reflect.TypeFor[Middleware](), method: wrapMethod, want: true,
		},
		{
			name: "[Fail] When: the type only promotes it from a field ; Should: not provide it",
			typ:  reflect.TypeFor[deepWrap](), method: wrapMethod, want: false,
		},
		{
			name: "[Fail] When: the type lacks the method ; Should: not provide it",
			typ:  reflect.TypeFor[doer](), method: wrapMethod, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, provides(tt.typ, tt.method))
		})
	}
}

func Test_hasMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		typ    reflect.Type
		method string
		want   bool
	}{
		{
			name: "[Success] When: a value receiver declares it ; Should: find it",
			typ:  reflect.TypeFor[doer](), method: executeMethod, want: true,
		},
		{
			name: "[Success] When: a pointer receiver declares it on a value type ; Should: find it",
			typ:  reflect.TypeFor[CheckFraud](), method: executeMethod, want: true,
		},
		{
			name: "[Success] When: the type is a pointer ; Should: find the method",
			typ:  reflect.TypeFor[*CheckFraud](), method: executeMethod, want: true,
		},
		{
			name: "[Success] When: an interface has it ; Should: find it",
			typ:  reflect.TypeFor[Middleware](), method: wrapMethod, want: true,
		},
		{
			name: "[Fail] When: the type lacks it ; Should: not find it",
			typ:  reflect.TypeFor[doer](), method: wrapMethod, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, hasMethod(tt.typ, tt.method))
		})
	}
}

func Test_derefType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  reflect.Type
		want reflect.Type
	}{
		{
			name: "[Success] When: the type is a pointer ; Should: return its element",
			typ:  reflect.TypeFor[*Reserve](), want: reflect.TypeFor[Reserve](),
		},
		{
			name: "[Success] When: the type is not a pointer ; Should: return it",
			typ:  reflect.TypeFor[Reserve](), want: reflect.TypeFor[Reserve](),
		},
		{
			name: "[Success] When: the type is a pointer to a pointer ; Should: remove one level",
			typ:  reflect.TypeFor[**Reserve](), want: reflect.TypeFor[*Reserve](),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, derefType(tt.typ))
		})
	}
}

func Test_structOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  reflect.Type
		want reflect.Type
	}{
		{
			name: "[Success] When: the type is a struct ; Should: return it",
			typ:  reflect.TypeFor[Reserve](), want: reflect.TypeFor[Reserve](),
		},
		{
			name: "[Success] When: the type points to a struct ; Should: return the struct",
			typ:  reflect.TypeFor[*Reserve](), want: reflect.TypeFor[Reserve](),
		},
		{
			name: "[Fail] When: the type is not a struct ; Should: return nil",
			typ:  reflect.TypeFor[int](), want: nil,
		},
		{
			name: "[Fail] When: the type is an interface ; Should: return nil",
			typ:  reflect.TypeFor[Middleware](), want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, structOf(tt.typ))
		})
	}
}

func Test_checkExecutionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		typ      reflect.Type
		wantErrs []error
		wantText []string
	}{
		{
			name: "[Success] When: the state has no ExecutionID ; Should: accept it",
			typ:  reflect.TypeFor[tripState](),
		},
		{
			name: "[Success] When: ExecutionID returns a string ; Should: accept it",
			typ:  reflect.TypeFor[goodIDState](),
		},
		{
			name: "[Fail] When: ExecutionID has another signature ; Should: name the type and the signature",
			typ:  reflect.TypeFor[badIDState](), wantErrs: []error{ErrInvalidState},
			wantText: []string{"badIDState: method ExecutionID is func() int, want func() string"},
		},
		{
			name: "[Fail] When: an unnamed type promotes a bad ExecutionID ; Should: name the type by its text",
			typ:  reflect.TypeFor[struct{ badIDState }](), wantErrs: []error{ErrInvalidState},
			wantText: []string{"struct { sagas.badIDState }: method ExecutionID"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			requireErrs(t, checkExecutionID(tt.typ), tt.wantErrs, tt.wantText)
		})
	}
}
