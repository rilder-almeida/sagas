package sagas

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	hotelOld  Hotel = "old"
	hotelMade Hotel = "made"
	hotelBack Hotel = "back"
	rateMade  Rate  = 9
	madeJSON        = `"made"`
)

type errorOnlyStep struct {
	Step[tripState, Hotel]
	doer
}

type noneOutputStep struct{ Step[tripState, None] }

func (noneOutputStep) Execute(context.Context, tripState) (None, error) { return None{}, nil }

type oldShapes struct {
	Step[tripState, Hotel]
	maker
}

func (oldShapes) Compensate(context.Context, *tripState) error { return nil }

func (oldShapes) Enabled(tripState) bool { return true }

func (oldShapes) Wrap(context.Context, Call) error { return nil }

type pivotAll struct {
	Pivot[tripState, None]
	doer
	undoer
	checker
	wrapper
}

type pivotWrongExecute struct{ Pivot[tripState, Hotel] }

func (pivotWrongExecute) Execute(context.Context, *tripState) error { return nil }

func makeHotel(context.Context, tripState) (Hotel, error) { return hotelMade, nil }

func makeRate(context.Context, tripState) (Rate, error) { return rateMade, nil }

func backHotel(context.Context, tripState) (Hotel, error) { return hotelBack, nil }

type bindRow struct {
	name           string
	flow           Flow
	field          int
	wantCompensate bool
	wantReplaces   bool
	wantEnabled    bool
	wantWrap       bool
	wantRules      ruleList
	wantCallOut    string
	wantCallOutErr error
	wantCallOutMsg string
	wantUndoOut    string
}

func runBindRow(t *testing.T, tt *bindRow) {
	t.Helper()

	sb, ok := tt.flow.(stateBinder[tripState])
	require.True(t, ok)

	m, rules := sb.bind(tt.flow, tt.field)

	assert.Equal(t, tt.wantRules, rules)
	assert.Equal(t, tt.wantCompensate, m.compensate != nil)
	assert.Equal(t, tt.wantReplaces, m.replaces)
	assert.Equal(t, tt.wantEnabled, m.enabled != nil)
	assert.Equal(t, tt.wantWrap, m.wrap != nil)

	start := tripState{Hotel: hotelOld, Rate: 1}
	state := start

	var callOut, undoOut encodedOutput

	if m.call != nil {
		out, err := m.call(t.Context(), &state)
		require.NoError(t, err)

		callOut = out
	}

	if m.compensate != nil {
		out, err := m.compensate(t.Context(), &state)
		require.NoError(t, err)

		undoOut = out
	}

	assert.Equal(t, tt.wantCallOut, string(callOut.data))
	require.ErrorIs(t, callOut.err, tt.wantCallOutErr)
	assert.Contains(t, errText(callOut.err), tt.wantCallOutMsg)
	assert.Equal(t, tt.wantUndoOut, string(undoOut.data))
	assert.Equal(t, start, state)
}

func nanRate(context.Context, tripState) (Rate, error) { return Rate(math.NaN()), nil }

type nanFeeStep struct{ Step[tripState, Charge] }

func (nanFeeStep) Execute(context.Context, tripState) (Charge, error) { return Charge(math.NaN()), nil }

func Test_typed_bind(t *testing.T) {
	t.Parallel()

	full := &fullStep{do: okAction, undo: okAction, pred: okPredicate}
	full.wrap = okMiddleware()

	all := &pivotAll{do: okAction, undo: okAction, pred: okPredicate}
	all.wrap = okMiddleware()

	tests := []bindRow{
		{
			name: "[Success] When: Execute returns a Hotel ; Should: return it encoded, not write it into the state",
			flow: producing("a", makeHotel), field: 0, wantCallOut: madeJSON,
		},
		{
			name: "[Success] When: Execute returns a Rate ; Should: return it encoded",
			flow: producingRate("a", makeRate), field: 1, wantCallOut: "9",
		},
		{
			name: "[Fail] When: Execute returns a Rate that does not encode ; Should: hand the encoding error over",
			flow: producingRate("a", nanRate), field: 1, wantCallOutErr: ErrInvalidState,
			wantCallOutMsg: "sagas: invalid state: Rate: ",
		},
		{
			name: "[Fail] When: the output field name differs from its type ; Should: name the field in the error",
			flow: &nanFeeStep{}, field: 2, wantCallOutErr: ErrInvalidState,
			wantCallOutMsg: "sagas: invalid state: Fee: ",
		},
		{
			name: "[Success] When: the output is None ; Should: return no bytes",
			flow: act("a", okAction), field: -1,
		},
		{
			name: "[Success] When: Compensate returns only an error ; Should: bind a restore that returns no bytes",
			flow: undoingMade("a", makeHotel, okAction), field: 0, wantCompensate: true, wantCallOut: madeJSON,
		},
		{
			name:           "[Success] When: Compensate returns a Hotel ; Should: bind a replace that encodes it",
			flow:           &replaceStep{make: makeHotel, replace: backHotel},
			wantCompensate: true,
			wantReplaces:   true,
			wantCallOut:    madeJSON,
			wantUndoOut:    `"back"`,
		},
		{
			name: "[Success] When: the step has every method ; Should: bind each of them",
			flow: full, field: -1, wantCompensate: true, wantEnabled: true, wantWrap: true,
		},
		{
			name: "[Fail] When: a Hotel step has Execute that returns only an error ; Should: name the shape it wants",
			flow: &errorOnlyStep{do: okAction},
			wantRules: ruleList{
				"method Execute of type *sagas.errorOnlyStep is func(context.Context, sagas.tripState) error, " +
					"want func(context.Context, sagas.tripState) (sagas.Hotel, error)",
			},
		},
		{
			name:  "[Fail] When: a None step has Execute that returns None too ; Should: name its shape",
			flow:  &noneOutputStep{},
			field: -1,
			wantRules: ruleList{
				"method Execute of type *sagas.noneOutputStep is func(context.Context, sagas.tripState) (sagas.None, " +
					"error), want func(context.Context, sagas.tripState) error",
			},
		},
		{
			name: "[Fail] When: Compensate, Enabled and Wrap have old shapes ; Should: name every accepted shape",
			flow: &oldShapes{make: makeHotel}, wantCallOut: madeJSON,
			wantRules: ruleList{
				"method Compensate of type *sagas.oldShapes is func(context.Context, *sagas.tripState) error, " +
					"want func(context.Context, sagas.tripState) error or " +
					"func(context.Context, sagas.tripState) (sagas.Hotel, error)",
				"method Enabled of type *sagas.oldShapes is func(sagas.tripState) bool, " +
					"want func(context.Context, sagas.tripState) bool",
				"method Wrap of type *sagas.oldShapes is func(context.Context, sagas.Call) error, " +
					"want func(context.Context, sagas.CallInfo, sagas.Call) error",
			},
		},
		{
			name: "[Success] When: the pivot returns a Hotel ; Should: return it encoded",
			flow: payingMade("p", makeHotel), wantCallOut: madeJSON,
		},
		{
			name: "[Success] When: the pivot output is None ; Should: return no bytes",
			flow: paying("p", okAction), field: -1,
		},
		{
			name: "[Success] When: the pivot has Compensate and Enabled ; Should: bind them for New to refuse once",
			flow: all, field: -1, wantCompensate: true, wantEnabled: true, wantWrap: true,
		},
		{
			name: "[Fail] When: the pivot Execute has the old shape ; Should: name the shape it wants",
			flow: &pivotWrongExecute{},
			wantRules: ruleList{
				"method Execute of type *sagas.pivotWrongExecute is func(context.Context, *sagas.tripState) error, " +
					"want func(context.Context, sagas.tripState) (sagas.Hotel, error)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runBindRow(t, &tt)
		})
	}
}

func Test_method(t *testing.T) {
	t.Parallel()

	restore := reflect.TypeFor[func(context.Context, tripState) error]()
	replace := reflect.TypeFor[func(context.Context, tripState) (Hotel, error)]()

	tests := []struct {
		name     string
		flow     Flow
		method   string
		accepted []reflect.Type
		want     ruleList
	}{
		{
			name: "[Success] When: the method has an accepted shape ; Should: add no rule",
			flow: &Reserve{}, method: executeMethod, accepted: []reflect.Type{restore},
		},
		{
			name: "[Success] When: the method is missing ; Should: add no rule",
			flow: &Reserve{}, method: "Compensate", accepted: []reflect.Type{restore},
		},
		{
			name: "[Fail] When: the method has another shape ; Should: name the shape it has and the one it wants",
			flow: &wrongStep{}, method: executeMethod, accepted: []reflect.Type{restore},
			want: ruleList{
				"method Execute of type *sagas.wrongStep is func(context.Context, *sagas.tripState) error, " +
					"want func(context.Context, sagas.tripState) error",
			},
		},
		{
			name: "[Fail] When: two shapes are accepted and neither matches ; Should: name both joined by or",
			flow: &oldShapes{}, method: "Compensate", accepted: []reflect.Type{restore, replace},
			want: ruleList{
				"method Compensate of type *sagas.oldShapes is func(context.Context, *sagas.tripState) error, " +
					"want func(context.Context, sagas.tripState) error or " +
					"func(context.Context, sagas.tripState) (sagas.Hotel, error)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, method(tt.flow, tt.method, tt.accepted...))
		})
	}
}
