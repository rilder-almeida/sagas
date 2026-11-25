package sagas

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_stateTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		typ      reflect.Type
		wantID   int
		wantErrs int
	}{
		{
			name:   "[Success] When: no field carries a tag ; Should: return -1",
			typ:    reflect.TypeFor[tripState](),
			wantID: -1,
		},
		{
			name:   `[Success] When: the second field carries sagas:"id" ; Should: return its index`,
			typ:    reflect.TypeFor[idTagState](),
			wantID: 1,
		},
		{
			name:   `[Success] When: a field carries sagas:"secret" only ; Should: return -1`,
			typ:    reflect.TypeFor[secretTagState](),
			wantID: -1,
		},
		{
			name:     `[Fail] When: two fields carry sagas:"id" ; Should: return -1 and one error`,
			typ:      reflect.TypeFor[twoIDState](),
			wantID:   -1,
			wantErrs: 1,
		},
		{
			name:     `[Fail] When: an int field and a string field both carry sagas:"id" ; Should: return both errors`,
			typ:      reflect.TypeFor[intThenStringIDState](),
			wantID:   -1,
			wantErrs: 2,
		},
		{
			name:     `[Fail] When: a field carries an unknown value and an id on an int ; Should: return both errors`,
			typ:      reflect.TypeFor[badTagsState](),
			wantID:   -1,
			wantErrs: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id, errs := stateTags(tt.typ)

			assert.Equal(t, tt.wantID, id)
			assert.Len(t, errs, tt.wantErrs)

			for _, err := range errs {
				require.ErrorIs(t, err, ErrInvalidState)
			}
		})
	}
}

type intThenStringIDState struct {
	A int    `sagas:"id"`
	B string `sagas:"id"`
}

type badTagsState struct {
	X string `sagas:"pii"`
	N int    `sagas:"id"`
}

func Test_tagKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  reflect.StructTag
		want []string
	}{
		{name: "[Success] When: the tag is empty ; Should: return no key", tag: ``},
		{name: "[Success] When: one pair ; Should: return its key", tag: tagLiteral, want: []string{"sagas"}},
		{
			name: "[Success] When: two pairs ; Should: return both keys in order",
			tag:  `json:"x,omitzero" sagas:"id,secret"`,
			want: []string{"json", "sagas"},
		},
		{name: "[Fail] When: a part has no colon ; Should: return the ? key", tag: `sagas`, want: []string{"?"}},
		{name: "[Fail] When: the value is not quoted ; Should: return the ? key", tag: `sagas:id`, want: []string{"?"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tagKeys(tt.tag))
		})
	}
}

func Test_tagOffense(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  reflect.StructTag
		top  bool
		want []offense
	}{
		{name: `[Success] When: sagas tag at the top level ; Should: return no offense`, tag: tagLiteral, top: true},
		{name: "[Success] When: no tag ; Should: return no offense", tag: ``, top: false},
		{
			name: `[Fail] When: sagas tag below the top level ; Should: return the level offense`,
			tag:  tagLiteral,
			want: []offense{{path: ".X", rule: "sagas tag below the top level"}},
		},
		{
			name: "[Fail] When: json and yaml keys together ; Should: return one struct tag offense",
			tag:  `json:"x" yaml:"x"`,
			top:  true,
			want: []offense{{path: ".X", rule: "struct tag"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tagOffense(".X", tt.tag, tt.top))
		})
	}
}

const tagLiteral = `sagas:"id"`

type valueMethodState struct{ ID string }

func (s valueMethodState) ExecutionID() string { return s.ID }

type pointerMethodState struct{ ID string }

func (s *pointerMethodState) ExecutionID() string { return s.ID }

type otherSignatureState struct{ ID string }

func (otherSignatureState) ExecutionID() int { return 0 }

type otherNameState struct{ ID string }

func (s otherNameState) Execution() string { return s.ID }

func Test_refuseExecutionIDMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		run      func() error
		wantText []string
	}{
		{
			name:     "[Fail] When: the state has the method on a value receiver ; Should: refuse it, name the tag",
			run:      refuseExecutionIDMethod[valueMethodState],
			wantText: []string{"valueMethodState", "ExecutionID", tagLiteral},
		},
		{
			name:     "[Fail] When: the state has the method on a pointer receiver ; Should: refuse it, name the tag",
			run:      refuseExecutionIDMethod[pointerMethodState],
			wantText: []string{"pointerMethodState", "ExecutionID", tagLiteral},
		},
		{
			name: "[Success] When: the method returns another type ; Should: accept the state",
			run:  refuseExecutionIDMethod[otherSignatureState],
		},
		{
			name: "[Success] When: the method has another name ; Should: accept the state",
			run:  refuseExecutionIDMethod[otherNameState],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.run()
			if len(tt.wantText) == 0 {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidState)

			for _, part := range tt.wantText {
				assert.Contains(t, err.Error(), part)
			}
		})
	}
}
