package sagas

import (
	"context"
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
	setName     = "set Name"
	cycleText   = "form a cycle"
	reserveName = "reserve"
	stepA       = `step "a"`
	quotedA     = `"a"`
	negAttempts = "Retry.Attempts is negative"
	badName     = "name may hold only ASCII letters"
	neitherText = "neither Execute nor Pivot"
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
		Name: "full", Description: "books the room", Retry: NoRetry(), AttemptTimeout: time.Second,
		CompensationFailure: CompensationHalt,
		do:                  okAction, undo: okAction, pred: okPredicate, wrap: okMiddleware(),
	}

	return []inspectRow{
		{
			name:    "[Success] When: a step sets every field and has every method ; Should: report all of them",
			inspect: trip, flow: full,
			wantReports: []StepReport{{
				Name: "full", Description: "books the room", Retry: NoRetry(), AttemptTimeout: time.Second,
				CompensationFailure: CompensationHalt, Execute: true, Compensate: true, Enabled: true, Wrap: true,
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
			inspect: trip, flow: &Reserve{Name: "book"},
			wantReports: []StepReport{{Name: "book", Execute: true}},
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
	compensable.CompensationFailure = CompensationHalt
	timed := act("a", okAction)
	timed.AttemptTimeout = -time.Second
	retried := act("a", okAction)
	retried.Retry = RetryPolicy{Attempts: -1}
	unknown := undoing("a", okAction, okAction)
	unknown.CompensationFailure = "stop"

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
			name:    "[Fail] When: CompensationFailure is set without Compensate ; Should: reject it",
			inspect: trip, flow: compensable,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{stepA, "CompensationFailure without Compensate"},
		},
		{
			name:    "[Fail] When: CompensationFailure is unknown ; Should: name the policy",
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
			name:    "[Fail] When: Retry has a negative field ; Should: name the field",
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
			wantContains: []string{"saga has no steps"},
		},
		{
			name:    "[Fail] When: the flow holds no step ; Should: report a saga without steps",
			inspect: trip, flow: Sequence(Parallel()),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"saga has no steps"},
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
