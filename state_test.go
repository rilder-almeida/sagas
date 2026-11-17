package sagas

import (
	"encoding/json/v2"
	"math"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scalarState struct {
	B   bool
	I   int
	I8  int8
	I16 int16
	I32 int32
	I64 int64
	U   uint
	U8  uint8
	U16 uint16
	U32 uint32
	U64 uint64
	P   uintptr
	F32 float32
	F64 float64
	S   string
}

type innerState struct {
	N int
}

type EmbeddedState struct {
	E string
}

type compositeState struct {
	EmbeddedState

	Arr    [2]int
	Sl     []string
	MS     map[string]int
	MI     map[int]string
	MF     map[float64]bool
	MT     map[netip.Addr]int
	Ptr    *int
	Nested innerState
}

type timeState struct {
	At time.Time
}

type node struct {
	Next *node
	Kids []node
}

type emptyState struct{}

type secretInner struct {
	secret int
}

type outer struct {
	Inner secretInner
}

type dashTagInner struct {
	X int `json:"-"`
}

type dashTagState struct {
	Inner dashTagInner
}

type nameTagInner struct {
	X int `json:"x"`
}

type nameTagState struct {
	Inner nameTagInner
}

type omitzeroTagInner struct {
	X int `json:",omitzero"`
}

type omitzeroTagState struct {
	Inner omitzeroTagInner
}

type funcState struct {
	F func()
}

type chanState struct {
	C chan int
}

type anyState struct {
	A any
}

type marshalerState struct {
	M json.Marshaler
}

type unsafeState struct {
	U unsafe.Pointer
}

type complexState struct {
	C complex128
}

type structKeyState struct {
	M map[innerState]int
}

type boolKeyState struct {
	M map[bool]int
}

type badValueState struct {
	M map[string]func()
}

type badElemState struct {
	S []chan int
}

type twoBadState struct {
	A func()
	B chan int
}

func Test_checkState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		typ       reflect.Type
		wantErr   error
		wantInMsg []string
	}{
		{
			name: "[Success] When: bool, every integer and float kind, uintptr and string ; Should: accept them",
			typ:  reflect.TypeFor[scalarState](),
		},
		{
			name: "[Success] When: array, slice, maps, pointer, nested and embedded struct ; Should: accept them",
			typ:  reflect.TypeFor[compositeState](),
		},
		{
			name: "[Success] When: a time.Time field ; Should: accept it without walking into it",
			typ:  reflect.TypeFor[timeState](),
		},
		{
			name: "[Success] When: a recursive type ; Should: end the walk and accept it",
			typ:  reflect.TypeFor[node](),
		},
		{
			name: "[Success] When: an empty struct ; Should: accept it",
			typ:  reflect.TypeFor[emptyState](),
		},
		{
			name:      "[Fail] When: the type is int ; Should: report not a struct",
			typ:       reflect.TypeFor[int](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"int", "not a struct"},
		},
		{
			name:      "[Fail] When: an unexported field at depth 2 ; Should: report its path as unexported",
			typ:       reflect.TypeOf(outer{Inner: secretInner{secret: 1}}),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"outer.Inner.secret", "unexported"},
		},
		{
			name:      `[Fail] When: a nested field carries json:"-" ; Should: report its path and the json tag`,
			typ:       reflect.TypeFor[dashTagState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"dashTagState.Inner.X", "json tag"},
		},
		{
			name:      `[Fail] When: a nested field carries json:"x" ; Should: report its path and the json tag`,
			typ:       reflect.TypeFor[nameTagState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"nameTagState.Inner.X", "json tag"},
		},
		{
			name:      `[Fail] When: a nested field carries json:",omitzero" ; Should: report its path and json tag`,
			typ:       reflect.TypeFor[omitzeroTagState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"omitzeroTagState.Inner.X", "json tag"},
		},
		{
			name:      "[Fail] When: a func field ; Should: report its path and the func kind",
			typ:       reflect.TypeFor[funcState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"funcState.F", "func"},
		},
		{
			name:      "[Fail] When: a chan field ; Should: report its path and the chan kind",
			typ:       reflect.TypeFor[chanState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"chanState.C", "chan"},
		},
		{
			name:      "[Fail] When: an any field ; Should: report its path and the interface kind",
			typ:       reflect.TypeFor[anyState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"anyState.A", "interface"},
		},
		{
			name:      "[Fail] When: a field typed as the json.Marshaler interface ; Should: report the interface kind",
			typ:       reflect.TypeFor[marshalerState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"marshalerState.M", "interface"},
		},
		{
			name:      "[Fail] When: an unsafe.Pointer field ; Should: report its path and the unsafe.Pointer kind",
			typ:       reflect.TypeFor[unsafeState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"unsafeState.U", "unsafe.Pointer"},
		},
		{
			name:      "[Fail] When: a complex128 field ; Should: report its path and the complex128 kind",
			typ:       reflect.TypeFor[complexState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"complexState.C", "complex128"},
		},
		{
			name:      "[Fail] When: a map with a struct key ; Should: report the key path",
			typ:       reflect.TypeFor[structKeyState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"structKeyState.M[key]"},
		},
		{
			name:      "[Fail] When: a map with a bool key ; Should: report the key path",
			typ:       reflect.TypeFor[boolKeyState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"boolKeyState.M[key]"},
		},
		{
			name:      "[Fail] When: a map with a func value ; Should: report the value path",
			typ:       reflect.TypeFor[badValueState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"badValueState.M[value]", "func"},
		},
		{
			name:      "[Fail] When: a slice of chan ; Should: report the element path",
			typ:       reflect.TypeFor[badElemState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"badElemState.S[]", "chan"},
		},
		{
			name:      "[Fail] When: two bad fields ; Should: report both paths in field order",
			typ:       reflect.TypeFor[twoBadState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"twoBadState.A", "twoBadState.B"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkState(tt.typ)
			require.ErrorIs(t, err, tt.wantErr)

			if err != nil {
				assertInOrder(t, err.Error(), tt.wantInMsg)
			}
		})
	}
}

func assertInOrder(t *testing.T, msg string, parts []string) {
	t.Helper()

	rest := msg
	for _, part := range parts {
		i := strings.Index(rest, part)
		if !assert.GreaterOrEqual(t, i, 0, "%q not found in order in %q", part, msg) {
			return
		}

		rest = rest[i+len(part):]
	}
}

func Test_checkState_roundTrip(t *testing.T) {
	t.Parallel()

	seats := 3
	tests := []struct {
		name  string
		value any
	}{
		{
			name: "[Success] When: a populated scalar struct ; Should: round-trip unchanged",
			value: scalarState{
				B: true, I: -1, I8: -8, I16: -16, I32: -32, I64: -64,
				U: 1, U8: 8, U16: 16, U32: 32, U64: 64, P: 7,
				F32: 1.5, F64: 2.25, S: "s",
			},
		},
		{
			name: "[Success] When: a populated composite struct ; Should: round-trip unchanged",
			value: compositeState{
				EmbeddedState: EmbeddedState{E: "e"},
				Arr:           [2]int{1, 2},
				Sl:            []string{"a"},
				MS:            map[string]int{"a": 1},
				MI:            map[int]string{1: "a"},
				MF:            map[float64]bool{1.5: true},
				MT:            map[netip.Addr]int{netip.MustParseAddr("10.0.0.1"): 1},
				Ptr:           &seats,
				Nested:        innerState{N: 4},
			},
		},
		{
			name:  "[Success] When: a time.Time field ; Should: round-trip unchanged",
			value: timeState{At: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)},
		},
		{
			name:  "[Success] When: a recursive value ; Should: round-trip unchanged",
			value: node{Next: &node{}, Kids: []node{{}}},
		},
		{
			name:  "[Success] When: an empty struct ; Should: round-trip unchanged",
			value: emptyState{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			typ := reflect.TypeOf(tt.value)
			require.NoError(t, checkState(typ))

			data, err := encodeField(tt.value)
			require.NoError(t, err)

			got := reflect.New(typ)
			require.NoError(t, decodeField(data, got.Interface()))
			assert.Equal(t, tt.value, got.Elem().Interface())
		})
	}
}

type encodeFixture struct {
	NilSlice   []int
	EmptySlice []int
	NilMap     map[string]int
	Ordered    map[string]int
}

type cyclic struct {
	Next *cyclic
}

func Test_encodeField(t *testing.T) {
	t.Parallel()

	loop := &cyclic{}
	loop.Next = loop

	tests := []struct {
		name    string
		value   any
		want    string
		wantErr error
	}{
		{
			name: "[Success] When: nil and empty containers and a map ; Should: keep nil as null and sort map keys",
			value: encodeFixture{
				EmptySlice: []int{},
				Ordered:    map[string]int{"c": 3, "a": 1, "b": 2},
			},
			want: `{"NilSlice":null,"EmptySlice":[],"NilMap":null,"Ordered":{"a":1,"b":2,"c":3}}`,
		},
		{
			name:    "[Fail] When: the value is NaN ; Should: return ErrInvalidState and no bytes",
			value:   math.NaN(),
			wantErr: ErrInvalidState,
		},
		{
			name:    "[Fail] When: the value holds a pointer cycle ; Should: return ErrInvalidState and no bytes",
			value:   loop,
			wantErr: ErrInvalidState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := encodeField(tt.value)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func Test_decodeField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		data    string
		want    encodeFixture
		wantErr error
	}{
		{
			name: "[Success] When: null and empty containers ; Should: keep nil as nil and empty as empty",
			data: `{"NilSlice":null,"EmptySlice":[],"NilMap":null,"Ordered":{"a":1}}`,
			want: encodeFixture{EmptySlice: []int{}, Ordered: map[string]int{"a": 1}},
		},
		{
			name:    "[Fail] When: the bytes are malformed ; Should: return ErrInvalidState",
			data:    `{"NilSlice":[1,`,
			wantErr: ErrInvalidState,
		},
		{
			name:    "[Fail] When: a value has the wrong JSON type ; Should: return ErrInvalidState",
			data:    `{"Ordered":{"a":"x"}}`,
			wantErr: ErrInvalidState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got encodeFixture

			err := decodeField([]byte(tt.data), &got)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
