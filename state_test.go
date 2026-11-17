package sagas

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math"
	"math/big"
	"net/netip"
	"reflect"
	"runtime/debug"
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
	PP        **int
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
		wantErr   error
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
			wantInMsg: []string{"int: not a struct"},
		},
		{
			name:      "[Fail] When: an unexported field at depth 2 ; Should: report its path as unexported",
			typ:       reflect.TypeOf(outer{Inner: secretInner{secret: 1}}),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"outer.Inner.secret: unexported field"},
		},
		{
			name:      `[Fail] When: a nested field carries json:"-" ; Should: report its path and the struct tag`,
			typ:       reflect.TypeFor[dashTagState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"dashTagState.Inner.X: struct tag"},
		},
		{
			name:      `[Fail] When: a nested field carries json:",omitzero" ; Should: report its path and the tag`,
			typ:       reflect.TypeFor[omitzeroTagState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"omitzeroTagState.Inner.X: struct tag"},
		},
		{
			name:      "[Fail] When: a nested field carries a yaml tag ; Should: report its path and the struct tag",
			typ:       reflect.TypeFor[yamlTagState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"yamlTagState.Inner.X: struct tag"},
		},
		{
			name:      "[Fail] When: an embedded struct value ; Should: report the embedded field",
			typ:       reflect.TypeOf(embeddedValueState{innerState{N: 1}}),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"embeddedValueState.innerState: embedded field"},
		},
		{
			name:      "[Fail] When: an embedded struct pointer ; Should: report the embedded field",
			typ:       reflect.TypeOf(embeddedPtrState{innerState: &innerState{N: 1}}),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"embeddedPtrState.innerState: embedded field"},
		},
		{
			name:      "[Fail] When: an any field ; Should: report its path and the interface kind",
			typ:       reflect.TypeFor[anyState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"anyState.A: rejected kind interface"},
		},
		{
			name:      "[Fail] When: a json.Marshaler interface field ; Should: report a standard library type",
			typ:       reflect.TypeFor[marshalerState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"marshalerState.M: standard library type json.Marshaler"},
		},
		{
			name:      "[Fail] When: a func field ; Should: report its path and the func kind",
			typ:       reflect.TypeFor[funcState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"funcState.F: rejected kind func"},
		},
		{
			name:      "[Fail] When: a chan field ; Should: report its path and the chan kind",
			typ:       reflect.TypeFor[chanState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"chanState.C: rejected kind chan"},
		},
		{
			name:      "[Fail] When: an unsafe.Pointer field ; Should: report a standard library type",
			typ:       reflect.TypeFor[unsafeState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"unsafeState.U: standard library type unsafe.Pointer"},
		},
		{
			name:      "[Fail] When: a complex128 field ; Should: report its path and the complex128 kind",
			typ:       reflect.TypeFor[complexState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"complexState.C: rejected kind complex128"},
		},
		{
			name:      "[Fail] When: a uintptr field ; Should: report its path and the uintptr kind",
			typ:       reflect.TypeFor[uintptrState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"uintptrState.P: rejected kind uintptr"},
		},
		{
			name:      "[Fail] When: a map with a float64 key ; Should: report the key path",
			typ:       reflect.TypeFor[floatKeyState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"floatKeyState.M[key]: map key must be a string or an integer"},
		},
		{
			name:      "[Fail] When: a map with a bool key ; Should: report the key path",
			typ:       reflect.TypeFor[boolKeyState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"boolKeyState.M[key]: map key must be a string or an integer"},
		},
		{
			name:      "[Fail] When: a map with a struct key ; Should: report the key path",
			typ:       reflect.TypeFor[structKeyState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"structKeyState.M[key]: map key must be a string or an integer"},
		},
		{
			name:      "[Fail] When: a map with a time.Duration key ; Should: report the key as standard library",
			typ:       reflect.TypeFor[stdKeyState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"stdKeyState.M[key]: standard library type time.Duration"},
		},
		{
			name:      "[Fail] When: a map key type with only MarshalText ; Should: report the key and the method",
			typ:       reflect.TypeFor[ownEncodingKeyState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"ownEncodingKeyState.M[key]: defines its own encoding with MarshalText"},
		},
		{
			name:      "[Fail] When: a time.Time field ; Should: report a standard library type",
			typ:       reflect.TypeFor[timeState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"timeState.At: standard library type time.Time"},
		},
		{
			name:      "[Fail] When: a time.Duration field ; Should: report a standard library type",
			typ:       reflect.TypeFor[durationState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"durationState.D: standard library type time.Duration"},
		},
		{
			name:      "[Fail] When: a *time.Duration field ; Should: report a standard library type",
			typ:       reflect.TypeFor[durationPtrState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"durationPtrState.D: standard library type time.Duration"},
		},
		{
			name:      "[Fail] When: a []time.Duration field ; Should: report the element as a standard library type",
			typ:       reflect.TypeFor[durationSliceState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"durationSliceState.D[]: standard library type time.Duration"},
		},
		{
			name:      "[Fail] When: a netip.Addr field ; Should: report a standard library type",
			typ:       reflect.TypeFor[addrState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"addrState.A: standard library type netip.Addr"},
		},
		{
			name:      "[Fail] When: a *big.Int field ; Should: report a standard library type",
			typ:       reflect.TypeFor[bigState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"bigState.B: standard library type big.Int"},
		},
		{
			name:    "[Fail] When: user types define one or more encoding methods ; Should: report each method",
			typ:     reflect.TypeFor[ownEncodingState](),
			wantErr: ErrInvalidState,
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
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"encodingRoot: defines its own encoding with MarshalJSON"},
		},
		{
			name:      "[Fail] When: the root type is time.Time ; Should: report a standard library type",
			typ:       reflect.TypeFor[time.Time](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"Time: standard library type time.Time"},
		},
		{
			name:      "[Fail] When: two bad fields ; Should: report both paths in field order",
			typ:       reflect.TypeFor[twoBadState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"twoBadState.A: rejected kind func", "twoBadState.B: rejected kind chan"},
		},
		{
			name:      "[Fail] When: the same bad type in two fields ; Should: report it under both paths",
			typ:       reflect.TypeFor[sameBadState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"sameBadState.A.C: rejected kind chan", "sameBadState.B.C: rejected kind chan"},
		},
		{
			name:      "[Fail] When: a pointer to a bad type ; Should: report the path through the pointer",
			typ:       reflect.TypeFor[ptrBadState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"ptrBadState.P.C: rejected kind chan"},
		},
		{
			name:      "[Fail] When: a slice of chan ; Should: report the element path",
			typ:       reflect.TypeFor[badElemState](),
			wantErr:   ErrInvalidState,
			wantInMsg: []string{"badElemState.S[]: rejected kind chan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkState(tt.typ)
			require.ErrorIs(t, err, tt.wantErr)

			msg := ""
			if err != nil {
				msg = err.Error()
			}

			assertInOrder(t, msg, tt.wantInMsg)
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

func Test_isStandardPath(t *testing.T) {
	t.Parallel()

	const app = "myapp"

	modules := buildModules{known: true, paths: []string{app, "example.com/lib"}}
	emptyMain := buildModules{known: true, paths: []string{}}
	tests := []struct {
		name    string
		path    string
		modules buildModules
		want    stdAnswer
	}{
		{
			name:    "[Success] When: time ; Should: be standard",
			path:    "time",
			modules: modules,
			want:    standard,
		},
		{
			name:    "[Success] When: net/netip ; Should: be standard",
			path:    "net/netip",
			modules: modules,
			want:    standard,
		},
		{
			name:    "[Success] When: a path under no module ; Should: be standard",
			path:    "example.com/other",
			modules: modules,
			want:    standard,
		},
		{
			name:    "[Success] When: a module path prefix without a slash ; Should: be standard",
			path:    "myappx",
			modules: modules,
			want:    standard,
		},
		{
			name:    "[Fail] When: the empty path ; Should: not be standard",
			path:    "",
			modules: modules,
			want:    notStandard,
		},
		{
			name:    "[Fail] When: main ; Should: not be standard",
			path:    "main",
			modules: modules,
			want:    notStandard,
		},
		{
			name:    "[Fail] When: the dotless main module ; Should: not be standard",
			path:    app,
			modules: modules,
			want:    notStandard,
		},
		{
			name:    "[Fail] When: a package of the dotless module ; Should: not be standard",
			path:    app + "/state",
			modules: modules,
			want:    notStandard,
		},
		{
			name:    "[Fail] When: a package of a dependency ; Should: not be standard",
			path:    "example.com/lib/x",
			modules: modules,
			want:    notStandard,
		},
		{
			name: "[Success] When: no build info and time ; Should: be unsure",
			path: "time",
			want: maybeStandard,
		},
		{
			name: "[Success] When: no build info and a dotless path ; Should: be unsure",
			path: app,
			want: maybeStandard,
		},
		{
			name: "[Fail] When: no build info and a dotted path ; Should: not be standard",
			path: "example.com/lib",
			want: notStandard,
		},
		{
			name: "[Fail] When: no build info and main ; Should: not be standard",
			path: "main",
			want: notStandard,
		},
		{
			name:    "[Success] When: build info with an empty main path and time ; Should: be standard",
			path:    "time",
			modules: emptyMain,
			want:    standard,
		},
		{
			name:    "[Success] When: build info with an empty main path and a dotless path ; Should: be standard",
			path:    app,
			modules: emptyMain,
			want:    standard,
		},
		{
			name:    "[Fail] When: build info with an empty main path and main ; Should: not be standard",
			path:    "main",
			modules: emptyMain,
			want:    notStandard,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, isStandardPath(tt.path, tt.modules))
		})
	}
}

func Test_standardRule(t *testing.T) {
	t.Parallel()

	modules := buildModules{known: true, paths: []string{"github.com/rilder-almeida/sagas"}}
	tests := []struct {
		name    string
		typ     reflect.Type
		modules buildModules
		want    string
	}{
		{
			name:    "[Success] When: a predeclared type ; Should: return no rule",
			typ:     reflect.TypeFor[int](),
			modules: modules,
		},
		{
			name:    "[Success] When: a type of the main module ; Should: return no rule",
			typ:     reflect.TypeFor[userInt](),
			modules: modules,
		},
		{
			name: "[Success] When: no build info and a dotted package path ; Should: return no rule",
			typ:  reflect.TypeFor[userInt](),
		},
		{
			name:    "[Fail] When: a standard library type ; Should: name it as a standard library type",
			typ:     reflect.TypeFor[time.Duration](),
			modules: modules,
			want:    "standard library type time.Duration",
		},
		{
			name:    "[Fail] When: build info with an empty main path ; Should: name a standard library type",
			typ:     reflect.TypeFor[time.Duration](),
			modules: buildModules{known: true, paths: []string{}},
			want:    "standard library type time.Duration",
		},
		{
			name: "[Fail] When: no build info and a dotless package path ; Should: say it cannot tell",
			typ:  reflect.TypeFor[time.Duration](),
			want: "cannot tell whether time.Duration is a standard library type without build info: " +
				"give the module a dotted path such as example.com/myapp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, standardRule(tt.typ, tt.modules))
		})
	}
}

func Test_modulePaths(t *testing.T) {
	t.Parallel()

	const (
		app  = "myapp"
		depA = "example.com/a"
	)

	tests := []struct {
		name string
		info *debug.BuildInfo
		want buildModules
	}{
		{
			name: "[Success] When: no build info ; Should: return no paths",
		},
		{
			name: "[Success] When: only the main module ; Should: return its path",
			info: &debug.BuildInfo{Main: debug.Module{Path: app}},
			want: buildModules{known: true, paths: []string{app}},
		},
		{
			name: "[Success] When: the main module and dependencies ; Should: return every path in order",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: app},
				Deps: []*debug.Module{{Path: depA}, {Path: "example.com/b"}},
			},
			want: buildModules{known: true, paths: []string{app, depA, "example.com/b"}},
		},
		{
			name: "[Success] When: a replaced dependency ; Should: return its original path",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: app},
				Deps: []*debug.Module{{Path: depA, Replace: &debug.Module{Path: "../fork"}}},
			},
			want: buildModules{known: true, paths: []string{app, depA}},
		},
		{
			name: "[Success] When: build info with an empty main path ; Should: know it and return no paths",
			info: &debug.BuildInfo{},
			want: buildModules{known: true, paths: []string{}},
		},
		{
			name: "[Success] When: an empty main path and a dependency ; Should: return the dependency only",
			info: &debug.BuildInfo{Deps: []*debug.Module{{Path: depA}}},
			want: buildModules{known: true, paths: []string{depA}},
		},
		{
			name: "[Success] When: a dependency with an empty path ; Should: ignore it",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: app},
				Deps: []*debug.Module{{Path: ""}, {Path: depA}},
			},
			want: buildModules{known: true, paths: []string{app, depA}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, modulePaths(tt.info))
		})
	}
}

func Test_checkState_roundTrip(t *testing.T) {
	t.Parallel()

	seats := 3
	seatsPtr := &seats
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
				PP:        &seatsPtr,
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

			again, err := encodeField(got.Elem().Interface())
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

	start := encodeFixture{NilSlice: []int{9}, NilMap: map[string]int{"z": 9}}
	tests := []struct {
		name    string
		data    string
		start   encodeFixture
		want    encodeFixture
		wantErr error
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
			name:    "[Fail] When: the bytes end before any member ; Should: return ErrInvalidState, keep dst",
			data:    `{`,
			start:   start,
			want:    start,
			wantErr: ErrInvalidState,
		},
		{
			name:    "[Fail] When: a later member has a wrong type ; Should: return ErrInvalidState, keep dst",
			data:    `{"EmptySlice":[1],"Ordered":"x"}`,
			start:   start,
			want:    start,
			wantErr: ErrInvalidState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.start

			err := decodeField([]byte(tt.data), &got)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_decodeTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dst     any
		wantErr error
	}{
		{
			name: "[Success] When: the destination is a non-nil pointer ; Should: return it",
			dst:  &encodeFixture{},
		},
		{
			name:    "[Fail] When: the destination is nil ; Should: return ErrInvalidState",
			dst:     nil,
			wantErr: ErrInvalidState,
		},
		{
			name:    "[Fail] When: the destination is not a pointer ; Should: return ErrInvalidState",
			dst:     encodeFixture{},
			wantErr: ErrInvalidState,
		},
		{
			name:    "[Fail] When: the destination is a nil pointer ; Should: return ErrInvalidState",
			dst:     (*encodeFixture)(nil),
			wantErr: ErrInvalidState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeTarget(tt.dst)
			require.ErrorIs(t, err, tt.wantErr)

			if err == nil {
				assert.Same(t, tt.dst, got.Interface())
			} else {
				assert.False(t, got.IsValid())
			}
		})
	}
}
