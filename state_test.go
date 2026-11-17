package sagas

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"image"
	"math"
	"math/big"
	"net/netip"
	"net/url"
	"reflect"
	"strconv"
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
	F32 float32
	F64 float64
	S   string
}

type userInt int

type namedState struct {
	N userInt
}

type innerState struct {
	N int
}

type compositeState struct {
	Arr       [2][3]int
	Sl        [][]string
	MS        map[string]int
	MI        map[int64]string
	MU        map[uint8]bool
	Ptr       *int
	Bytes     []byte
	Nested    innerState
	NestedPtr *innerState
}

type emptiesState struct {
	NilBytes   []byte
	EmptyBytes []byte
	NilSlice   []int
	EmptySlice []int
	NilMap     map[string]int
	EmptyMap   map[string]int
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

type omitzeroTagInner struct {
	X int `json:",omitzero"`
}

type omitzeroTagState struct {
	Inner omitzeroTagInner
}

type yamlTagInner struct {
	X int `yaml:"x"`
}

type yamlTagState struct {
	Inner yamlTagInner
}

type embeddedValueState struct {
	innerState
}

type embeddedPtrState struct {
	*innerState
}

type anyState struct {
	A any
}

type marshalerState struct {
	M json.Marshaler
}

type funcState struct {
	F func()
}

type chanState struct {
	C chan int
}

type unsafeState struct {
	U unsafe.Pointer
}

type complexState struct {
	C complex128
}

type uintptrState struct {
	P uintptr
}

type floatKeyState struct {
	M map[float64]int
}

type boolKeyState struct {
	M map[bool]int
}

type structKeyState struct {
	M map[innerState]int
}

type stdKeyState struct {
	M map[time.Duration]int
}

type ownEncodingKeyState struct {
	M map[onlyMarshalText]int
}

type timeState struct {
	At time.Time
}

type durationState struct {
	D time.Duration
}

type durationPtrState struct {
	D *time.Duration
}

type durationSliceState struct {
	D []time.Duration
}

type addrState struct {
	A netip.Addr
}

type bigState struct {
	B *big.Int
}

type ptrPtrState struct {
	P **int
}

type ptrSliceState struct {
	P *[]int
}

type ptrMapState struct {
	P *map[string]int
}

type slicePtrSliceState struct {
	S []*[]int
}

type rawValueState struct {
	V jsontext.Value
}

type urlState struct {
	U url.URL
}

type monthState struct {
	M time.Month
}

type pointState struct {
	P image.Point
}

type diamond1 struct {
	A chan int
	B chan int
}

type diamond2 struct {
	A diamond1
	B diamond1
}

type diamond3 struct {
	A diamond2
	B diamond2
}

type mutualA struct {
	B *mutualB
	X chan int
}

type mutualB struct {
	A *mutualA
	Y chan int
}

type mutualState struct {
	B mutualB
	A mutualA
}

type onlyMarshalJSON int

func (onlyMarshalJSON) MarshalJSON() ([]byte, error) { return []byte("0"), nil }

type onlyUnmarshalJSON int

func (*onlyUnmarshalJSON) UnmarshalJSON([]byte) error { return nil }

type onlyMarshalJSONTo int

func (onlyMarshalJSONTo) MarshalJSONTo(*jsontext.Encoder) error { return nil }

type onlyUnmarshalJSONFrom int

func (*onlyUnmarshalJSONFrom) UnmarshalJSONFrom(*jsontext.Decoder) error { return nil }

type onlyMarshalText int

func (onlyMarshalText) MarshalText() ([]byte, error) { return []byte("0"), nil }

type onlyUnmarshalText int

func (*onlyUnmarshalText) UnmarshalText([]byte) error { return nil }

type onlyAppendText int

func (onlyAppendText) AppendText(b []byte) ([]byte, error) { return b, nil }

type bothJSON int

func (bothJSON) MarshalJSON() ([]byte, error) { return []byte("0"), nil }

func (*bothJSON) UnmarshalJSON([]byte) error { return nil }

type bothText int

func (bothText) MarshalText() ([]byte, error) { return []byte("0"), nil }

func (*bothText) UnmarshalText([]byte) error { return nil }

type ownEncodingState struct {
	MJ  onlyMarshalJSON
	UJ  onlyUnmarshalJSON
	MT  onlyMarshalText
	UT  onlyUnmarshalText
	AT  onlyAppendText
	BJ  bothJSON
	BT  bothText
	MJT onlyMarshalJSONTo
	UJF onlyUnmarshalJSONFrom
}

type encodingRoot struct {
	N int
}

func (encodingRoot) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

func (*encodingRoot) UnmarshalJSON([]byte) error { return nil }

type twoBadState struct {
	A func()
	B chan int
}

type badInner struct {
	C chan int
}

type sameBadState struct {
	A badInner
	B badInner
}

type ptrBadState struct {
	P *badInner
}

type badElemState struct {
	S []chan int
}

func Test_checkState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		typ       reflect.Type
		wantInMsg []string
	}{
		{
			name: "[Success] When: bool, every integer kind, float32, float64 and string ; Should: accept them",
			typ:  reflect.TypeFor[scalarState](),
		},
		{
			name: "[Success] When: a named user type over int ; Should: accept it",
			typ:  reflect.TypeFor[namedState](),
		},
		{
			name: "[Success] When: nested arrays, slices, plain maps, pointers and []byte ; Should: accept them",
			typ:  reflect.TypeFor[compositeState](),
		},
		{
			name: "[Success] When: a time.Month field ; Should: accept it",
			typ:  reflect.TypeFor[monthState](),
		},
		{
			name: "[Success] When: an image.Point field ; Should: accept it",
			typ:  reflect.TypeFor[pointState](),
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
			wantInMsg: []string{"int: not a struct"},
		},
		{
			name:      "[Fail] When: an unexported field at depth 2 ; Should: report its path as unexported",
			typ:       reflect.TypeOf(outer{Inner: secretInner{secret: 1}}),
			wantInMsg: []string{"outer.Inner.secret: unexported field"},
		},
		{
			name:      `[Fail] When: a nested field carries json:"-" ; Should: report its path and the struct tag`,
			typ:       reflect.TypeFor[dashTagState](),
			wantInMsg: []string{"dashTagState.Inner.X: struct tag"},
		},
		{
			name:      `[Fail] When: a nested field carries json:",omitzero" ; Should: report its path and the tag`,
			typ:       reflect.TypeFor[omitzeroTagState](),
			wantInMsg: []string{"omitzeroTagState.Inner.X: struct tag"},
		},
		{
			name:      "[Fail] When: a nested field carries a yaml tag ; Should: report its path and the struct tag",
			typ:       reflect.TypeFor[yamlTagState](),
			wantInMsg: []string{"yamlTagState.Inner.X: struct tag"},
		},
		{
			name:      "[Fail] When: an embedded struct value ; Should: report the embedded field",
			typ:       reflect.TypeOf(embeddedValueState{innerState{N: 1}}),
			wantInMsg: []string{"embeddedValueState.innerState: embedded field"},
		},
		{
			name:      "[Fail] When: an embedded struct pointer ; Should: report the embedded field",
			typ:       reflect.TypeOf(embeddedPtrState{innerState: &innerState{N: 1}}),
			wantInMsg: []string{"embeddedPtrState.innerState: embedded field"},
		},
		{
			name:      "[Fail] When: an any field ; Should: report its path and the interface kind",
			typ:       reflect.TypeFor[anyState](),
			wantInMsg: []string{"anyState.A: rejected kind interface"},
		},
		{
			name:      "[Fail] When: a json.Marshaler interface field ; Should: report the interface kind",
			typ:       reflect.TypeFor[marshalerState](),
			wantInMsg: []string{"marshalerState.M: rejected kind interface"},
		},
		{
			name:      "[Fail] When: a func field ; Should: report its path and the func kind",
			typ:       reflect.TypeFor[funcState](),
			wantInMsg: []string{"funcState.F: rejected kind func"},
		},
		{
			name:      "[Fail] When: a chan field ; Should: report its path and the chan kind",
			typ:       reflect.TypeFor[chanState](),
			wantInMsg: []string{"chanState.C: rejected kind chan"},
		},
		{
			name:      "[Fail] When: an unsafe.Pointer field ; Should: report the unsafe.Pointer kind",
			typ:       reflect.TypeFor[unsafeState](),
			wantInMsg: []string{"unsafeState.U: rejected kind unsafe.Pointer"},
		},
		{
			name:      "[Fail] When: a complex128 field ; Should: report its path and the complex128 kind",
			typ:       reflect.TypeFor[complexState](),
			wantInMsg: []string{"complexState.C: rejected kind complex128"},
		},
		{
			name:      "[Fail] When: a uintptr field ; Should: report its path and the uintptr kind",
			typ:       reflect.TypeFor[uintptrState](),
			wantInMsg: []string{"uintptrState.P: rejected kind uintptr"},
		},
		{
			name:      "[Fail] When: a map with a float64 key ; Should: report the key path",
			typ:       reflect.TypeFor[floatKeyState](),
			wantInMsg: []string{"floatKeyState.M[key]: map key must be a string or an integer"},
		},
		{
			name:      "[Fail] When: a map with a bool key ; Should: report the key path",
			typ:       reflect.TypeFor[boolKeyState](),
			wantInMsg: []string{"boolKeyState.M[key]: map key must be a string or an integer"},
		},
		{
			name:      "[Fail] When: a map with a struct key ; Should: report the key path",
			typ:       reflect.TypeFor[structKeyState](),
			wantInMsg: []string{"structKeyState.M[key]: map key must be a string or an integer"},
		},
		{
			name:      "[Fail] When: a map with a time.Duration key ; Should: report the key as a rejected type",
			typ:       reflect.TypeFor[stdKeyState](),
			wantInMsg: []string{"stdKeyState.M[key]: rejected type time.Duration"},
		},
		{
			name:      "[Fail] When: a map key type with only MarshalText ; Should: report the key and the method",
			typ:       reflect.TypeFor[ownEncodingKeyState](),
			wantInMsg: []string{"ownEncodingKeyState.M[key]: defines its own encoding with MarshalText"},
		},
		{
			name:      "[Fail] When: a time.Time field ; Should: report its MarshalJSON method",
			typ:       reflect.TypeFor[timeState](),
			wantInMsg: []string{"timeState.At: defines its own encoding with MarshalJSON"},
		},
		{
			name:      "[Fail] When: a time.Duration field ; Should: report a rejected type",
			typ:       reflect.TypeFor[durationState](),
			wantInMsg: []string{"durationState.D: rejected type time.Duration"},
		},
		{
			name:      "[Fail] When: a *time.Duration field ; Should: report a rejected type",
			typ:       reflect.TypeFor[durationPtrState](),
			wantInMsg: []string{"durationPtrState.D: rejected type time.Duration"},
		},
		{
			name:      "[Fail] When: a []time.Duration field ; Should: report the element as a rejected type",
			typ:       reflect.TypeFor[durationSliceState](),
			wantInMsg: []string{"durationSliceState.D[]: rejected type time.Duration"},
		},
		{
			name:      "[Fail] When: a jsontext.Value field ; Should: report a rejected type",
			typ:       reflect.TypeFor[rawValueState](),
			wantInMsg: []string{"rawValueState.V: rejected type jsontext.Value"},
		},
		{
			name: "[Fail] When: a url.URL field with unexported fields inside ; Should: report each of them",
			typ:  reflect.TypeFor[urlState](),
			wantInMsg: []string{
				"urlState.U.User.username: unexported field",
				"urlState.U.User.password: unexported field",
				"urlState.U.User.passwordSet: unexported field",
			},
		},
		{
			name:      "[Fail] When: a netip.Addr field ; Should: report its MarshalText method",
			typ:       reflect.TypeFor[addrState](),
			wantInMsg: []string{"addrState.A: defines its own encoding with MarshalText"},
		},
		{
			name:      "[Fail] When: a *big.Int field ; Should: report its MarshalJSON method",
			typ:       reflect.TypeFor[bigState](),
			wantInMsg: []string{"bigState.B: defines its own encoding with MarshalJSON"},
		},
		{
			name:      "[Fail] When: a **int field ; Should: report the pointer rule",
			typ:       reflect.TypeFor[ptrPtrState](),
			wantInMsg: []string{"ptrPtrState.P: pointer to a pointer, slice or map"},
		},
		{
			name:      "[Fail] When: a *[]int field ; Should: report the pointer rule",
			typ:       reflect.TypeFor[ptrSliceState](),
			wantInMsg: []string{"ptrSliceState.P: pointer to a pointer, slice or map"},
		},
		{
			name:      "[Fail] When: a *map[string]int field ; Should: report the pointer rule",
			typ:       reflect.TypeFor[ptrMapState](),
			wantInMsg: []string{"ptrMapState.P: pointer to a pointer, slice or map"},
		},
		{
			name:      "[Fail] When: a []*[]int field ; Should: report the element under the pointer rule",
			typ:       reflect.TypeFor[slicePtrSliceState](),
			wantInMsg: []string{"slicePtrSliceState.S[]: pointer to a pointer, slice or map"},
		},
		{
			name: "[Fail] When: a three-level diamond with a bad leaf ; Should: report each of the 8 paths once",
			typ:  reflect.TypeFor[diamond3](),
			wantInMsg: []string{
				"diamond3.A.A.A: rejected kind chan",
				"diamond3.A.A.B: rejected kind chan",
				"diamond3.A.B.A: rejected kind chan",
				"diamond3.A.B.B: rejected kind chan",
				"diamond3.B.A.A: rejected kind chan",
				"diamond3.B.A.B: rejected kind chan",
				"diamond3.B.B.A: rejected kind chan",
				"diamond3.B.B.B: rejected kind chan",
			},
		},
		{
			name: "[Fail] When: user types define one or more encoding methods ; Should: report each method",
			typ:  reflect.TypeFor[ownEncodingState](),
			wantInMsg: []string{
				"ownEncodingState.MJ: defines its own encoding with MarshalJSON",
				"ownEncodingState.UJ: defines its own encoding with UnmarshalJSON",
				"ownEncodingState.MT: defines its own encoding with MarshalText",
				"ownEncodingState.UT: defines its own encoding with UnmarshalText",
				"ownEncodingState.AT: defines its own encoding with AppendText",
				"ownEncodingState.BJ: defines its own encoding with MarshalJSON",
				"ownEncodingState.BT: defines its own encoding with MarshalText",
				"ownEncodingState.MJT: defines its own encoding with MarshalJSONTo",
				"ownEncodingState.UJF: defines its own encoding with UnmarshalJSONFrom",
			},
		},
		{
			name:      "[Fail] When: the root type defines its own encoding ; Should: report the root",
			typ:       reflect.TypeOf(encodingRoot{N: 1}),
			wantInMsg: []string{"encodingRoot: defines its own encoding with MarshalJSON"},
		},
		{
			name:      "[Fail] When: the root type is time.Time ; Should: report its MarshalJSON method",
			typ:       reflect.TypeFor[time.Time](),
			wantInMsg: []string{"Time: defines its own encoding with MarshalJSON"},
		},
		{
			name:      "[Fail] When: two bad fields ; Should: report both paths in field order",
			typ:       reflect.TypeFor[twoBadState](),
			wantInMsg: []string{"twoBadState.A: rejected kind func", "twoBadState.B: rejected kind chan"},
		},
		{
			name:      "[Fail] When: the same bad type in two fields ; Should: report it under both paths",
			typ:       reflect.TypeFor[sameBadState](),
			wantInMsg: []string{"sameBadState.A.C: rejected kind chan", "sameBadState.B.C: rejected kind chan"},
		},
		{
			name:      "[Fail] When: a pointer to a bad type ; Should: report the path through the pointer",
			typ:       reflect.TypeFor[ptrBadState](),
			wantInMsg: []string{"ptrBadState.P.C: rejected kind chan"},
		},
		{
			name:      "[Fail] When: a slice of chan ; Should: report the element path",
			typ:       reflect.TypeFor[badElemState](),
			wantInMsg: []string{"badElemState.S[]: rejected kind chan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkState(tt.typ)
			assert.Equal(t, len(tt.wantInMsg) > 0, errors.Is(err, ErrInvalidState), err)

			msg := errText(err)
			assertInOrder(t, msg, tt.wantInMsg)
			assert.Equal(t, len(tt.wantInMsg), strings.Count(msg, ErrInvalidState.Error()), msg)
		})
	}
}

func Test_typeWalk_walk(t *testing.T) {
	t.Parallel()

	const chanRule = "rejected kind chan"

	tests := []struct {
		name string
		typ  reflect.Type
		want []offense
	}{
		{
			name: "[Success] When: a recursive type ; Should: return no offense",
			typ:  reflect.TypeFor[node](),
		},
		{
			name: "[Fail] When: one bad type in two fields ; Should: return it under both relative paths",
			typ:  reflect.TypeFor[sameBadState](),
			want: []offense{{path: ".A.C", rule: chanRule}, {path: ".B.C", rule: chanRule}},
		},
		{
			name: "[Fail] When: two mutually recursive bad types from two fields ; Should: return every path",
			typ:  reflect.TypeFor[mutualState](),
			want: []offense{
				{path: ".B.A.X", rule: chanRule},
				{path: ".B.Y", rule: chanRule},
				{path: ".A.B.Y", rule: chanRule},
				{path: ".A.X", rule: chanRule},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := typeWalk{onPath: map[reflect.Type]bool{}, done: map[reflect.Type][]offense{}}
			assert.Equal(t, tt.want, w.walk(tt.typ))
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

func Test_definesEncoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{
			name: "[Success] When: a plain int ; Should: report no method",
			typ:  reflect.TypeFor[int](),
		},
		{
			name: "[Success] When: a plain struct ; Should: report no method",
			typ:  reflect.TypeFor[innerState](),
		},
		{
			name: "[Success] When: the json.Marshaler interface itself ; Should: report no method",
			typ:  reflect.TypeFor[json.Marshaler](),
		},
		{
			name: "[Fail] When: only MarshalJSON on the value ; Should: report MarshalJSON",
			typ:  reflect.TypeFor[onlyMarshalJSON](),
			want: "MarshalJSON",
		},
		{
			name: "[Fail] When: only UnmarshalJSON on the pointer ; Should: report UnmarshalJSON",
			typ:  reflect.TypeFor[onlyUnmarshalJSON](),
			want: "UnmarshalJSON",
		},
		{
			name: "[Fail] When: only MarshalJSONTo ; Should: report MarshalJSONTo",
			typ:  reflect.TypeFor[onlyMarshalJSONTo](),
			want: "MarshalJSONTo",
		},
		{
			name: "[Fail] When: only UnmarshalJSONFrom on the pointer ; Should: report UnmarshalJSONFrom",
			typ:  reflect.TypeFor[onlyUnmarshalJSONFrom](),
			want: "UnmarshalJSONFrom",
		},
		{
			name: "[Fail] When: only MarshalText ; Should: report MarshalText",
			typ:  reflect.TypeFor[onlyMarshalText](),
			want: "MarshalText",
		},
		{
			name: "[Fail] When: only UnmarshalText on the pointer ; Should: report UnmarshalText",
			typ:  reflect.TypeFor[onlyUnmarshalText](),
			want: "UnmarshalText",
		},
		{
			name: "[Fail] When: only AppendText ; Should: report AppendText",
			typ:  reflect.TypeFor[onlyAppendText](),
			want: "AppendText",
		},
		{
			name: "[Fail] When: both text methods ; Should: report MarshalText first",
			typ:  reflect.TypeFor[bothText](),
			want: "MarshalText",
		},
		{
			name: "[Fail] When: time.Time ; Should: report MarshalJSON first",
			typ:  reflect.TypeFor[time.Time](),
			want: "MarshalJSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, definesEncoding(tt.typ))
		})
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
			name: "[Success] When: scalars at their extremes ; Should: round-trip unchanged",
			value: scalarState{
				B: true, I: -1, I8: math.MinInt8, I16: math.MinInt16, I32: math.MinInt32, I64: math.MinInt64,
				U: 1, U8: math.MaxUint8, U16: math.MaxUint16, U32: math.MaxUint32, U64: math.MaxUint64,
				F32: math.MaxFloat32, F64: math.Copysign(0, -1), S: "sé\n",
			},
		},
		{
			name:  "[Success] When: a named user type over int ; Should: round-trip unchanged",
			value: namedState{N: 42},
		},
		{
			name: "[Success] When: a populated composite struct ; Should: round-trip unchanged",
			value: compositeState{
				Arr:       [2][3]int{{1, 2, 3}, {4, 5, 6}},
				Sl:        [][]string{{"a"}, nil, {}},
				MS:        map[string]int{"a": 1, "b": 2},
				MI:        map[int64]string{math.MinInt64: "min", 1: "one"},
				MU:        map[uint8]bool{255: true},
				Ptr:       &seats,
				Bytes:     []byte{0, 1, 255},
				Nested:    innerState{N: 4},
				NestedPtr: &innerState{N: 5},
			},
		},
		{
			name: "[Success] When: nil and empty bytes, slices and maps ; Should: keep nil and empty apart",
			value: emptiesState{
				EmptyBytes: []byte{},
				EmptySlice: []int{},
				EmptyMap:   map[string]int{},
			},
		},
		{
			name:  "[Success] When: a time.Month ; Should: round-trip unchanged",
			value: monthState{M: time.December},
		},
		{
			name:  "[Success] When: an image.Point ; Should: round-trip unchanged",
			value: pointState{P: image.Point{X: -3, Y: math.MaxInt}},
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

			data, err := encodeField("State", tt.value)
			require.NoError(t, err)

			got := reflect.New(typ)
			require.NoError(t, decodeField("State", data, got.Interface()))
			assert.Equal(t, tt.value, got.Elem().Interface())

			again, err := encodeField("State", got.Elem().Interface())
			require.NoError(t, err)
			assert.Equal(t, string(data), string(again))
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

func jsonCause(err error) reflect.Type {
	if semantic, ok := errors.AsType[*json.SemanticError](err); ok {
		return reflect.TypeOf(semantic)
	}

	if syntactic, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		return reflect.TypeOf(syntactic)
	}

	return nil
}

func Test_encodeField(t *testing.T) {
	t.Parallel()

	loop := &cyclic{}
	loop.Next = loop

	tests := []struct {
		name      string
		value     any
		want      string
		wantErr   error
		wantInMsg []string
		wantCause reflect.Type
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
			name:      "[Fail] When: the value is NaN ; Should: return ErrInvalidState and no bytes",
			value:     math.NaN(),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{valueInvalid},
			wantCause: reflect.TypeFor[*json.SemanticError](),
		},
		{
			name:      "[Fail] When: the value holds a pointer cycle ; Should: return ErrInvalidState and no bytes",
			value:     loop,
			wantErr:   ErrInvalidState,
			wantInMsg: []string{valueInvalid},
			wantCause: reflect.TypeFor[*json.SemanticError](),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := encodeField("Value", tt.value)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantCause, jsonCause(err))
			assertInOrder(t, errText(err), tt.wantInMsg)
			assert.LessOrEqual(t, strings.Count(errText(err), ErrInvalidState.Error()), 1)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func Test_decodeField(t *testing.T) {
	t.Parallel()

	start := encodeFixture{NilSlice: []int{9}, NilMap: map[string]int{"z": 9}}
	tests := []struct {
		name      string
		data      string
		start     encodeFixture
		want      encodeFixture
		wantErr   error
		wantInMsg []string
		wantCause reflect.Type
	}{
		{
			name: "[Success] When: null and empty containers ; Should: keep nil as nil and empty as empty",
			data: `{"NilSlice":null,"EmptySlice":[],"NilMap":null,"Ordered":{"a":1}}`,
			want: encodeFixture{EmptySlice: []int{}, Ordered: map[string]int{"a": 1}},
		},
		{
			name:  "[Success] When: the destination holds values the bytes omit ; Should: replace it whole",
			data:  `{"Ordered":{"a":1}}`,
			start: start,
			want:  encodeFixture{Ordered: map[string]int{"a": 1}},
		},
		{
			name:      "[Fail] When: the bytes end before any member ; Should: return ErrInvalidState, keep dst",
			data:      `{`,
			start:     start,
			want:      start,
			wantErr:   ErrInvalidState,
			wantInMsg: []string{valueInvalid},
			wantCause: reflect.TypeFor[*jsontext.SyntacticError](),
		},
		{
			name:      "[Fail] When: a later member has a wrong type ; Should: return ErrInvalidState, keep dst",
			data:      `{"EmptySlice":[1],"Ordered":"x"}`,
			start:     start,
			want:      start,
			wantErr:   ErrInvalidState,
			wantInMsg: []string{valueInvalid},
			wantCause: reflect.TypeFor[*json.SemanticError](),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.start

			err := decodeField("Value", []byte(tt.data), &got)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantCause, jsonCause(err))
			assertInOrder(t, errText(err), tt.wantInMsg)
			assert.LessOrEqual(t, strings.Count(errText(err), ErrInvalidState.Error()), 1)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_decodeTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dst       any
		wantErr   error
		wantInMsg []string
	}{
		{
			name: "[Success] When: the destination is a non-nil pointer ; Should: return it",
			dst:  &encodeFixture{},
		},
		{
			name:      "[Fail] When: the destination is nil ; Should: return ErrInvalidState, name the field",
			dst:       nil,
			wantErr:   ErrInvalidState,
			wantInMsg: []string{valueInvalid + "decode destination <nil>"},
		},
		{
			name:      "[Fail] When: the destination is not a pointer ; Should: return ErrInvalidState, name the field",
			dst:       encodeFixture{},
			wantErr:   ErrInvalidState,
			wantInMsg: []string{valueInvalid + "decode destination sagas.encodeFixture"},
		},
		{
			name:      "[Fail] When: the destination is a nil pointer ; Should: return ErrInvalidState, name the field",
			dst:       (*encodeFixture)(nil),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{valueInvalid + "decode destination *sagas.encodeFixture"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeTarget("Value", tt.dst)
			require.ErrorIs(t, err, tt.wantErr)
			assertInOrder(t, errText(err), tt.wantInMsg)

			if err == nil {
				assert.Same(t, tt.dst, got.Interface())
			} else {
				assert.False(t, got.IsValid())
			}
		})
	}
}

type chargeState struct {
	Total float64
}

type linkState struct {
	Next *linkState
}

type testState struct {
	Seats  int
	Charge chargeState
	Tags   map[string]int
	Items  []string
	At     int64
	Link   *linkState
}

const (
	valueInvalid = "sagas: invalid state: Value: "
	seatsField   = "Seats"
	chargeField  = "Charge"
	itemsField   = "Items"
	linkField    = "Link"
	firstStep    = "reserve"
	secondStep   = "charge"
	probeStep    = "probe"
)

func baseState() testState {
	return testState{
		Seats:  2,
		Charge: chargeState{Total: 1.5},
		Tags:   map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6},
		At:     1_700_000_000_000_000_000,
	}
}

func probeState() testState {
	return testState{
		Seats:  -1,
		Charge: chargeState{Total: -1},
		Tags:   map[string]int{"probe": 1},
		Items:  []string{"probe"},
		At:     -1,
		Link:   &linkState{Next: &linkState{}},
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func assertMoved(t *testing.T, st *store[testState], snap snapshot[testState], want []string) {
	t.Helper()

	_, err := st.commit(probeStep, snap, probeState())
	if len(want) == 0 {
		require.NoError(t, err)

		return
	}

	require.ErrorIs(t, err, ErrConflict)

	msg := err.Error()
	assert.Equal(t, len(want), strings.Count(msg, `changed by step "probe"`), msg)

	for _, field := range want {
		assert.Contains(t, msg, "field "+field+` changed by step "probe"`)
	}
}

func Test_newStore(t *testing.T) {
	t.Parallel()

	require.NoError(t, checkState(reflect.TypeFor[testState]()))

	nan := baseState()
	nan.Charge.Total = math.NaN()

	loop := &linkState{}
	loop.Next = loop
	cycle := baseState()
	cycle.Link = loop

	tests := []struct {
		name      string
		initial   testState
		wantErr   error
		wantInMsg []string
	}{
		{
			name:    "[Success] When: a valid state ; Should: accept a commit of every field from its first snapshot",
			initial: baseState(),
		},
		{
			name:      "[Fail] When: a NaN at depth 2 ; Should: return ErrInvalidState that names the path",
			initial:   nan,
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"sagas: invalid state: Charge: ", "Total"},
		},
		{
			name:      "[Fail] When: a pointer cycle ; Should: return ErrInvalidState that names the field",
			initial:   cycle,
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"sagas: invalid state: Link: "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st, err := newStore(tt.initial)
			require.ErrorIs(t, err, tt.wantErr)
			assertInOrder(t, errText(err), tt.wantInMsg)
			assert.LessOrEqual(t, strings.Count(errText(err), ErrInvalidState.Error()), 1)

			if err != nil {
				assert.Nil(t, st)

				return
			}

			var got testState
			require.NoError(t, st.writeBack(&got))
			assert.Equal(t, tt.initial, got)
			assertMoved(t, st, st.snapshot(), nil)
		})
	}
}

func storeErr[S any](initial S) func() (any, error) {
	return func() (any, error) {
		st, err := newStore(initial)

		return st, err
	}
}

func Test_newStore_invalidType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		create    func() (any, error)
		wantInMsg string
	}{
		{
			name:      "[Fail] When: the state has a chan field ; Should: return ErrInvalidState and no store",
			create:    storeErr(chanState{C: make(chan int)}),
			wantInMsg: "sagas: invalid state: chanState.C: rejected kind chan",
		},
		{
			name:      "[Fail] When: the state has an unexported field ; Should: return ErrInvalidState and no store",
			create:    storeErr(outer{Inner: secretInner{secret: 1}}),
			wantInMsg: "sagas: invalid state: outer.Inner.secret: unexported field",
		},
		{
			name:      "[Fail] When: the state has a **int field ; Should: return ErrInvalidState and no store",
			create:    storeErr(ptrPtrState{}),
			wantInMsg: "sagas: invalid state: ptrPtrState.P: pointer to a pointer, slice or map",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st, err := tt.create()
			require.ErrorIs(t, err, ErrInvalidState)
			assert.Equal(t, tt.wantInMsg, err.Error())
			assert.Nil(t, st)
		})
	}
}

type copyInner struct {
	N int
}

type copyFixture struct {
	Scalar     int
	NilSlice   []int
	EmptySlice []int
	NilMap     map[string]int
	EmptyMap   map[string]int
	Ptr        *int
	Nested     copyInner
	At         int64
	SharedA    *copyInner
	SharedB    *copyInner
}

func newCopyFixture() copyFixture {
	seats := 3
	shared := &copyInner{N: 1}

	return copyFixture{
		Scalar:     1,
		EmptySlice: []int{},
		EmptyMap:   map[string]int{},
		Ptr:        &seats,
		Nested:     copyInner{N: 2},
		At:         1_700_000_000_000_000_000,
		SharedA:    shared,
		SharedB:    shared,
	}
}

func Test_copyState(t *testing.T) {
	t.Parallel()

	require.NoError(t, checkState(reflect.TypeFor[copyFixture]()))

	st, err := newStore(newCopyFixture())
	require.NoError(t, err)

	base, err := copyState(st.snapshot())
	require.NoError(t, err)
	assert.Equal(t, newCopyFixture(), *base)
	assert.Nil(t, base.NilSlice)
	assert.Nil(t, base.NilMap)
	assert.NotNil(t, base.EmptySlice)
	assert.Empty(t, base.EmptySlice)
	assert.NotNil(t, base.EmptyMap)
	assert.Empty(t, base.EmptyMap)
	assert.Equal(t, newCopyFixture().At, base.At)
	assert.NotSame(t, base.SharedA, base.SharedB)

	tests := []struct {
		name   string
		mutate func(*copyFixture)
	}{
		{
			name:   "[Success] When: the copy changes a scalar ; Should: leave the next copy unchanged",
			mutate: func(c *copyFixture) { c.Scalar = 9 },
		},
		{
			name:   "[Success] When: the copy appends to a nil slice ; Should: leave the next copy nil",
			mutate: func(c *copyFixture) { c.NilSlice = append(c.NilSlice, 9) },
		},
		{
			name:   "[Success] When: the copy appends to an empty slice ; Should: leave the next copy empty",
			mutate: func(c *copyFixture) { c.EmptySlice = append(c.EmptySlice, 9) },
		},
		{
			name:   "[Success] When: the copy sets a nil map ; Should: leave the next copy nil",
			mutate: func(c *copyFixture) { c.NilMap = map[string]int{"x": 9} },
		},
		{
			name:   "[Success] When: the copy writes an empty map ; Should: leave the next copy empty",
			mutate: func(c *copyFixture) { c.EmptyMap["x"] = 9 },
		},
		{
			name:   "[Success] When: the copy writes through a pointer ; Should: leave the next target unchanged",
			mutate: func(c *copyFixture) { *c.Ptr = 9 },
		},
		{
			name:   "[Success] When: the copy changes a nested struct ; Should: leave the next copy unchanged",
			mutate: func(c *copyFixture) { c.Nested.N = 9 },
		},
		{
			name:   "[Success] When: the copy changes a Unix nanosecond time ; Should: leave the next copy unchanged",
			mutate: func(c *copyFixture) { c.At++ },
		},
		{
			name:   "[Success] When: the copy writes through a shared pointer ; Should: leave the next copy unchanged",
			mutate: func(c *copyFixture) { c.SharedA.N = 9 },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			snap := st.snapshot()

			first, firstErr := copyState(snap)
			require.NoError(t, firstErr)

			tt.mutate(first)

			second, secondErr := copyState(snap)
			require.NoError(t, secondErr)
			assert.Equal(t, newCopyFixture(), *second)
		})
	}
}

func Test_copyState_malformedSnapshot(t *testing.T) {
	t.Parallel()

	require.NoError(t, checkState(reflect.TypeFor[copyFixture]()))

	st, err := newStore(newCopyFixture())
	require.NoError(t, err)

	tests := []struct {
		name      string
		overrides map[string]string
		wantInMsg []string
	}{
		{
			name:      "[Fail] When: one field holds malformed bytes ; Should: return ErrInvalidState that names it",
			overrides: map[string]string{"Nested": `{`},
			wantInMsg: []string{"sagas: invalid state: Nested: "},
		},
		{
			name:      "[Fail] When: one field holds JSON of the wrong type ; Should: return ErrInvalidState, name it",
			overrides: map[string]string{"Scalar": `"x"`},
			wantInMsg: []string{"sagas: invalid state: Scalar: "},
		},
		{
			name:      "[Fail] When: two fields hold bad bytes ; Should: name both, the first field in front",
			overrides: map[string]string{"Nested": `{`, "Scalar": `"x"`},
			wantInMsg: []string{"sagas: invalid state: Scalar: ", "sagas: invalid state: Nested: "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			snap := st.snapshot()

			for name, data := range tt.overrides {
				field, found := reflect.TypeFor[copyFixture]().FieldByName(name)
				require.True(t, found)

				snap.fields[field.Index[0]] = []byte(data)
			}

			got, copyErr := copyState(snap)
			require.ErrorIs(t, copyErr, ErrInvalidState)
			assert.Nil(t, got)
			assertInOrder(t, copyErr.Error(), tt.wantInMsg)
			assert.Equal(t, len(tt.overrides), strings.Count(copyErr.Error(), ErrInvalidState.Error()))
		})
	}
}

func Test_store_commit(t *testing.T) {
	t.Parallel()

	require.NoError(t, checkState(reflect.TypeFor[testState]()))

	reserved := func(s *testState) { s.Seats = 5 }
	committed := baseState()
	reserved(&committed)

	tests := []struct {
		name                     string
		earlier                  []func(*testState)
		secondMutate             func(*testState)
		secondSnapshotAfterFirst bool
		wantChanged              []string
		wantErr                  error
		wantInMsg                []string
		wantMoved                []string
		wantState                testState
	}{
		{
			name:         "[Success] When: parallel snapshots change disjoint fields ; Should: commit both",
			earlier:      []func(*testState){reserved},
			secondMutate: func(s *testState) { s.Charge.Total = 10 },
			wantChanged:  []string{chargeField},
			wantMoved:    []string{seatsField, chargeField},
			wantState: func() testState {
				s := committed
				s.Charge.Total = 10

				return s
			}(),
		},
		{
			name:         "[Fail] When: parallel snapshots change one field ; Should: return ErrConflict, both steps",
			earlier:      []func(*testState){reserved},
			secondMutate: func(s *testState) { s.Seats = 6 },
			wantErr:      ErrConflict,
			wantInMsg:    []string{"field Seats", `step "charge"`, `step "reserve"`},
			wantMoved:    []string{seatsField},
			wantState:    committed,
		},
		{
			name:                     "[Success] When: the same field changes in sequence ; Should: commit both",
			earlier:                  []func(*testState){reserved},
			secondMutate:             func(s *testState) { s.Seats = 6 },
			secondSnapshotAfterFirst: true,
			wantChanged:              []string{seatsField},
			wantMoved:                []string{seatsField},
			wantState: func() testState {
				s := baseState()
				s.Seats = 6

				return s
			}(),
		},
		{
			name:    "[Fail] When: a conflict and a free change in one commit ; Should: write neither",
			earlier: []func(*testState){reserved},
			secondMutate: func(s *testState) {
				s.Items = append(s.Items, "x")
				s.Seats = 6
			},
			wantErr:   ErrConflict,
			wantInMsg: []string{"field Seats", `step "charge"`, `step "reserve"`},
			wantMoved: []string{seatsField},
			wantState: committed,
		},
		{
			name:         "[Success] When: the attempt changes nothing ; Should: return an empty list, keep versions",
			earlier:      []func(*testState){reserved},
			secondMutate: func(*testState) {},
			wantChanged:  []string{},
			wantMoved:    []string{seatsField},
			wantState:    committed,
		},
		{
			name: "[Success] When: float, map, nil slice and time change in parallel ; Should: never conflict",
			earlier: []func(*testState){func(s *testState) {
				s.Charge.Total = 2.5
				s.Tags["g"] = 7
				s.Items = append(s.Items, "x")
				s.At++
			}},
			secondMutate: func(s *testState) { s.Link = &linkState{} },
			wantChanged:  []string{linkField},
			wantMoved:    []string{chargeField, "Tags", itemsField, "At", linkField},
			wantState: func() testState {
				s := baseState()
				s.Charge.Total = 2.5
				s.Tags["g"] = 7
				s.Items = []string{"x"}
				s.At++
				s.Link = &linkState{}

				return s
			}(),
		},
		{
			name:    "[Fail] When: the second step writes NaN ; Should: return ErrInvalidState, name the path",
			earlier: []func(*testState){func(s *testState) { s.Tags["a"] = 10 }},
			secondMutate: func(s *testState) {
				s.Seats = 6
				s.Charge.Total = math.NaN()
			},
			secondSnapshotAfterFirst: true,
			wantErr:                  ErrInvalidState,
			wantInMsg:                []string{"sagas: invalid state: Charge: ", "Total"},
			wantState: func() testState {
				s := baseState()
				s.Tags["a"] = 10

				return s
			}(),
		},
		{
			name:         "[Success] When: a nil slice is set to empty ; Should: count the field as changed",
			earlier:      []func(*testState){reserved},
			secondMutate: func(s *testState) { s.Items = []string{} },
			wantChanged:  []string{itemsField},
			wantMoved:    []string{seatsField, itemsField},
			wantState: func() testState {
				s := committed
				s.Items = []string{}

				return s
			}(),
		},
		{
			name:    "[Success] When: three fields change out of order ; Should: list them in declaration order",
			earlier: []func(*testState){reserved},
			secondMutate: func(s *testState) {
				s.Link = &linkState{}
				s.Items = []string{"x"}
				s.Charge.Total = 10
			},
			secondSnapshotAfterFirst: true,
			wantChanged:              []string{chargeField, itemsField, linkField},
			wantMoved:                []string{chargeField, itemsField, linkField},
			wantState: func() testState {
				s := committed
				s.Charge.Total = 10
				s.Items = []string{"x"}
				s.Link = &linkState{}

				return s
			}(),
		},
		{
			name:         "[Fail] When: a field goes A to B to A after the snapshot ; Should: return ErrConflict",
			earlier:      []func(*testState){reserved, func(s *testState) { s.Seats = 2 }},
			secondMutate: func(s *testState) { s.Seats = 6 },
			wantErr:      ErrConflict,
			wantInMsg:    []string{"field Seats", `step "charge"`, `step "reserve"`},
			wantMoved:    []string{seatsField},
			wantState:    baseState(),
		},
		{
			name:    "[Fail] When: the second step makes Link a self-loop ; Should: return ErrInvalidState, no write",
			earlier: []func(*testState){reserved},
			secondMutate: func(s *testState) {
				s.Seats = 6
				s.Link = &linkState{}
				s.Link.Next = s.Link
			},
			secondSnapshotAfterFirst: true,
			wantErr:                  ErrInvalidState,
			wantInMsg:                []string{"sagas: invalid state: Link: "},
			wantState:                committed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st, err := newStore(baseState())
			require.NoError(t, err)

			early := st.snapshot()

			for _, mutate := range tt.earlier {
				snap := st.snapshot()
				attempt, copyErr := copyState(snap)
				require.NoError(t, copyErr)

				mutate(attempt)

				_, commitErr := st.commit(firstStep, snap, *attempt)
				require.NoError(t, commitErr)
			}

			second := early
			if tt.secondSnapshotAfterFirst {
				second = st.snapshot()
			}

			attempt, err := copyState(second)
			require.NoError(t, err)

			tt.secondMutate(attempt)

			changed, err := st.commit(secondStep, second, *attempt)
			require.ErrorIs(t, err, tt.wantErr)
			assertInOrder(t, errText(err), tt.wantInMsg)
			assert.Equal(t, tt.wantChanged, changed)

			var got testState
			require.NoError(t, st.writeBack(&got))
			assert.Equal(t, tt.wantState, got)

			assertMoved(t, st, second, tt.wantMoved)
		})
	}
}

func Test_store_writeBack(t *testing.T) {
	t.Parallel()

	require.NoError(t, checkState(reflect.TypeFor[testState]()))

	tests := []struct {
		name    string
		dst     *testState
		want    *testState
		wantErr error
	}{
		{
			name: "[Success] When: the destination holds other values ; Should: keep the committed state",
			dst:  &testState{Seats: 99, Items: []string{"stale"}},
			want: func() *testState {
				s := baseState()
				s.Seats = 5

				return &s
			}(),
		},
		{
			name:    "[Fail] When: the destination is nil ; Should: return ErrInvalidState",
			wantErr: ErrInvalidState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st, err := newStore(baseState())
			require.NoError(t, err)

			snap := st.snapshot()
			attempt, err := copyState(snap)
			require.NoError(t, err)

			attempt.Seats = 5
			_, err = st.commit(firstStep, snap, *attempt)
			require.NoError(t, err)

			require.ErrorIs(t, st.writeBack(tt.dst), tt.wantErr)

			later := st.snapshot()
			next, err := copyState(later)
			require.NoError(t, err)

			next.Seats = 7
			next.Tags["a"] = 100
			_, err = st.commit(secondStep, later, *next)
			require.NoError(t, err)

			assert.Equal(t, tt.want, tt.dst)
		})
	}
}

type benchSmall struct {
	A int
	B float64
	C string
	D bool
}

type benchRow struct {
	ID   int
	Name string
}

type benchLarge struct {
	Index map[string]int
	Rows  []benchRow
	Count int
}

func newBenchLarge() benchLarge {
	large := benchLarge{Index: make(map[string]int, 1000), Rows: make([]benchRow, 0, 1000)}

	for i := range 1000 {
		name := "row-" + strconv.Itoa(i)
		large.Index[name] = i
		large.Rows = append(large.Rows, benchRow{ID: i, Name: name})
	}

	return large
}

func BenchmarkStore_commit(b *testing.B) {
	b.Run("small", func(b *testing.B) {
		benchCommit(b, benchSmall{A: 1, B: 2.5, C: "seat", D: true}, func(s *benchSmall) { s.A++ })
	})
	b.Run("large", func(b *testing.B) {
		benchCommit(b, newBenchLarge(), func(s *benchLarge) { s.Count++ })
	})
}

func benchCommit[S any](b *testing.B, initial S, change func(*S)) {
	b.Helper()

	require.NoError(b, checkState(reflect.TypeFor[S]()))

	st, err := newStore(initial)
	require.NoError(b, err)

	b.ReportAllocs()

	for b.Loop() {
		snap := st.snapshot()

		attempt, copyErr := copyState(snap)
		require.NoError(b, copyErr)

		change(attempt)

		_, commitErr := st.commit("bench", snap, *attempt)
		require.NoError(b, commitErr)
	}
}
