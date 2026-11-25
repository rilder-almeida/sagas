package sagas

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reportOf(name string, after ...string) StepReport {
	return StepReport{Name: name, After: after}
}

func okSteps(names ...string) []*actStep {
	steps := make([]*actStep, 0, len(names))
	for _, n := range names {
		steps = append(steps, act(n, okAction))
	}

	return steps
}

func Test_Sequence(t *testing.T) {
	t.Parallel()

	s := okSteps("a", "b", "c", "d")
	a, b, c, d := s[0], s[1], s[2], s[3]

	tests := []struct {
		name        string
		flow        Flow
		wantReports []StepReport
	}{
		{
			name:        "[Success] When: three steps form a chain ; Should: make each wait for the one before",
			flow:        Sequence(a, b, c),
			wantReports: []StepReport{reportOf("a"), reportOf("b", "a"), reportOf("c", "b")},
		},
		{
			name:        "[Success] When: a step follows a parallel group (fan-in) ; Should: wait for every member",
			flow:        Sequence(Parallel(a, b), c),
			wantReports: []StepReport{reportOf("a"), reportOf("b"), reportOf("c", "a", "b")},
		},
		{
			name:        "[Success] When: a parallel group follows a step (fan-out) ; Should: make every member wait",
			flow:        Sequence(a, Parallel(b, c)),
			wantReports: []StepReport{reportOf("a"), reportOf("b", "a"), reportOf("c", "a")},
		},
		{
			name: "[Success] When: a group sits between two steps ; Should: draw a diamond",
			flow: Sequence(a, Parallel(b, c), d),
			wantReports: []StepReport{
				reportOf("a"), reportOf("b", "a"), reportOf("c", "a"), reportOf("d", "b", "c"),
			},
		},
		{
			name: "[Success] When: two sequences follow each other ; Should: join the end of one to the next",
			flow: Sequence(Sequence(a, b), Sequence(c, d)),
			wantReports: []StepReport{
				reportOf("a"), reportOf("b", "a"), reportOf("c", "b"), reportOf("d", "c"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Inspect[tripState](tt.flow)
			require.NoError(t, err)
			assert.Equal(t, tt.wantReports, got)
		})
	}
}

func Test_Parallel(t *testing.T) {
	t.Parallel()

	s := okSteps("a", "b", "c", "d")
	a, b, c, d := s[0], s[1], s[2], s[3]

	tests := []struct {
		name        string
		flow        Flow
		wantReports []StepReport
	}{
		{
			name:        "[Success] When: two steps run in parallel ; Should: make neither wait",
			flow:        Parallel(a, b),
			wantReports: []StepReport{reportOf("a"), reportOf("b")},
		},
		{
			name: "[Success] When: two chains run in parallel ; Should: keep each chain apart",
			flow: Parallel(Sequence(a, b), Sequence(c, d)),
			wantReports: []StepReport{
				reportOf("a"), reportOf("b", "a"), reportOf("c"), reportOf("d", "c"),
			},
		},
		{
			name:        "[Success] When: one pointer ends two chains ; Should: report it once with both dependencies",
			flow:        Parallel(Sequence(a, c), Sequence(b, c)),
			wantReports: []StepReport{reportOf("a"), reportOf("c", "a", "b"), reportOf("b")},
		},
		{
			name:        "[Success] When: one pointer is given twice ; Should: report it once",
			flow:        Parallel(a, a),
			wantReports: []StepReport{reportOf("a")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Inspect[tripState](tt.flow)
			require.NoError(t, err)
			assert.Equal(t, tt.wantReports, got)
		})
	}
}

func Test_resolve(t *testing.T) {
	t.Parallel()

	s := okSteps("a", "b", "c")
	a, b, c := s[0], s[1], s[2]

	var (
		typedNil *actStep
		nilGroup *Group
	)

	tests := []struct {
		name        string
		flow        Flow
		wantSteps   []binder
		wantParents [][]int
		wantErr     string
	}{
		{
			name: "[Success] When: the flow is nil ; Should: find no step",
		},
		{
			name: "[Success] When: the flow is a typed nil pointer ; Should: find no step",
			flow: typedNil,
		},
		{
			name:        "[Success] When: the flow is one step ; Should: find it with no parent",
			flow:        a,
			wantSteps:   []binder{a},
			wantParents: [][]int{{}},
		},
		{
			name:        "[Success] When: a step follows two steps ; Should: link it to both in flow order",
			flow:        Sequence(Parallel(b, a), c),
			wantSteps:   []binder{b, a, c},
			wantParents: [][]int{{}, {}, {0, 1}},
		},
		{
			name:        "[Success] When: a step waits for itself ; Should: link it to itself",
			flow:        Sequence(a, a),
			wantSteps:   []binder{a},
			wantParents: [][]int{{0}},
		},
		{
			name:        "[Fail] When: a Sequence item is nil ; Should: report its position",
			flow:        Sequence(a, nil),
			wantSteps:   []binder{a},
			wantParents: [][]int{{}},
			wantErr:     "Sequence item at index 1 is nil",
		},
		{
			name:        "[Fail] When: an empty Sequence sits in a Sequence ; Should: report its position",
			flow:        Sequence(a, Sequence(), b),
			wantSteps:   []binder{a, b},
			wantParents: [][]int{{}, {0}},
			wantErr:     "Sequence item at index 1 is an empty Sequence",
		},
		{
			name:        "[Fail] When: an empty Parallel sits in a Parallel ; Should: report its position",
			flow:        Parallel(Parallel(), a),
			wantSteps:   []binder{a},
			wantParents: [][]int{{}},
			wantErr:     "Parallel item at index 0 is an empty Parallel",
		},
		{
			name: "[Success] When: the flow is an empty Sequence ; Should: find no step and no error",
			flow: Sequence(),
		},
		{
			name: "[Success] When: the flow is a nil Group ; Should: find no step",
			flow: nilGroup,
		},
		{
			name:        "[Fail] When: an item embeds *Step ; Should: report its type and position",
			flow:        Sequence(a, &sharedA{Step: &Step[tripState, None]{}, do: okAction}),
			wantSteps:   []binder{a},
			wantParents: [][]int{{}},
			wantErr:     "Sequence item at index 1 has type *sagas.sharedA",
		},
		{
			name:        "[Fail] When: a Parallel item is a nil Group ; Should: report its position",
			flow:        Parallel(a, nilGroup),
			wantSteps:   []binder{a},
			wantParents: [][]int{{}},
			wantErr:     "Parallel item at index 1 is nil",
		},
		{
			name:        "[Fail] When: a nested Parallel item is a typed nil pointer ; Should: report its position",
			flow:        Sequence(a, Parallel(b, typedNil)),
			wantSteps:   []binder{a, b},
			wantParents: [][]int{{}, {0}},
			wantErr:     "Parallel item at index 1 is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resolve(tt.flow)
			assert.Equal(t, tt.wantSteps, got.flows)
			assert.Equal(t, tt.wantParents, got.parents)
			assert.Equal(t, tt.wantErr != "", len(got.errs) > 0)
			assert.Contains(t, errText(errors.Join(got.errs...)), tt.wantErr)
		})
	}
}

func Test_resolution_visit(t *testing.T) {
	t.Parallel()

	var typedNil *actStep

	tests := []struct {
		name string
		flow Flow
		at   string
		want ends
	}{
		{
			name: "[Success] When: the item is a step ; Should: return it as head and tail",
			flow: act("a", okAction), at: "flow", want: ends{heads: []int{0}, tails: []int{0}},
		},
		{
			name: "[Success] When: the item is a group ; Should: return the ends of the group",
			flow: Sequence(act("a", okAction), act("b", okAction)), at: "flow",
			want: ends{heads: []int{0}, tails: []int{1}},
		},
		{
			name: "[Fail] When: the item is nil ; Should: return no ends",
			flow: nil, at: "Sequence item at index 2", want: ends{},
		},
		{
			name: "[Fail] When: the item is a typed nil pointer ; Should: return no ends",
			flow: typedNil, at: "flow", want: ends{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := resolution{index: map[Flow]int{}}

			assert.Equal(t, tt.want, r.visit(tt.flow, tt.at))
		})
	}
}

func Test_resolution_group(t *testing.T) {
	t.Parallel()

	s := okSteps("a", "b", "c")
	a, b, c := s[0], s[1], s[2]

	tests := []struct {
		name  string
		group *Group
		want  ends
	}{
		{
			name:  "[Success] When: the group is a Sequence ; Should: start at the first item and end at the last",
			group: Sequence(a, b, c),
			want:  ends{heads: []int{0}, tails: []int{2}},
		},
		{
			name:  "[Success] When: the group is a Parallel ; Should: start and end at every item",
			group: Parallel(a, b),
			want:  ends{heads: []int{0, 1}, tails: []int{0, 1}},
		},
		{
			name:  "[Success] When: a Sequence ends in a Parallel ; Should: end at every member",
			group: Sequence(a, Parallel(b, c)),
			want:  ends{heads: []int{0}, tails: []int{1, 2}},
		},
		{
			name:  "[Success] When: the group is empty ; Should: return no ends",
			group: Sequence(),
			want:  ends{},
		},
		{
			name:  "[Fail] When: an empty group sits between two steps ; Should: return the ends of the steps",
			group: Sequence(a, Parallel(), b),
			want:  ends{heads: []int{0}, tails: []int{1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := resolution{index: map[Flow]int{}}

			assert.Equal(t, tt.want, r.group(tt.group))
		})
	}
}

func Test_resolution_step(t *testing.T) {
	t.Parallel()

	s := okSteps("a", "b")
	a, b := s[0], s[1]

	tests := []struct {
		name  string
		flows []Flow
		want  []ends
	}{
		{
			name:  "[Success] When: two pointers are given ; Should: give each its own index",
			flows: []Flow{a, b},
			want:  []ends{{heads: []int{0}, tails: []int{0}}, {heads: []int{1}, tails: []int{1}}},
		},
		{
			name:  "[Success] When: one pointer is given twice ; Should: give it one index",
			flows: []Flow{a, b, a},
			want: []ends{
				{heads: []int{0}, tails: []int{0}},
				{heads: []int{1}, tails: []int{1}},
				{heads: []int{0}, tails: []int{0}},
			},
		},
		{
			name:  "[Fail] When: the item embeds *Step ; Should: return no ends and keep the next index",
			flows: []Flow{&sharedA{Step: &Step[tripState, None]{}, do: okAction}, a},
			want:  []ends{{}, {heads: []int{0}, tails: []int{0}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := resolution{index: map[Flow]int{}}
			got := make([]ends, 0, len(tt.flows))

			for _, f := range tt.flows {
				got = append(got, r.step(f, "flow"))
			}

			assert.Equal(t, tt.want, got)
		})
	}
}

type pivotBesideStepField struct {
	Pivot[tripState, None]
	doer

	Step string
}

func Test_embedsStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		typ       reflect.Type
		want      bool
		wantEmbed reflect.Type
	}{
		{
			name: "[Success] When: a pointer to a struct embeds Step ; Should: accept it",
			typ:  reflect.TypeFor[*Reserve](), want: true, wantEmbed: reflect.TypeFor[Step[tripState, None]](),
		},
		{
			name: "[Success] When: Step comes through a struct embedded by value ; Should: accept it",
			typ:  reflect.TypeFor[*nestedReserve](), want: true, wantEmbed: reflect.TypeFor[Step[tripState, None]](),
		},
		{
			name: "[Success] When: a pointer to a struct embeds Pivot ; Should: accept it",
			typ:  reflect.TypeFor[*pivotStep](), want: true, wantEmbed: reflect.TypeFor[Pivot[tripState, None]](),
		},
		{
			name:      "[Success] When: a Pivot embed sits beside a plain field named Step ; Should: accept the Pivot",
			typ:       reflect.TypeFor[*pivotBesideStepField](),
			want:      true,
			wantEmbed: reflect.TypeFor[Pivot[tripState, None]](),
		},
		{
			name: "[Fail] When: the struct embeds *Pivot ; Should: reject it",
			typ:  reflect.TypeFor[*struct{ *Pivot[tripState, None] }](), want: false,
		},
		{
			name: "[Fail] When: the type is a struct, not a pointer ; Should: reject it",
			typ:  reflect.TypeFor[Reserve](), want: false,
		},
		{
			name: "[Fail] When: the pointer is to a non-struct ; Should: reject it",
			typ:  reflect.TypeFor[*int](), want: false,
		},
		{
			name: "[Fail] When: the struct embeds *Step ; Should: reject it",
			typ:  reflect.TypeFor[*sharedA](), want: false,
		},
		{
			name: "[Fail] When: Step comes through an embedded pointer ; Should: reject it",
			typ:  reflect.TypeFor[*ptrReserve](), want: false,
		},
		{
			name: "[Fail] When: a named field is called Step ; Should: reject it",
			typ:  reflect.TypeFor[*struct{ Step Step[tripState, None] }](), want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embed, ok := embedsStep(tt.typ)

			assert.Equal(t, tt.want, ok)
			assert.Equal(t, tt.wantEmbed, embed.Type)
		})
	}
}

func Test_shortType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{
			name: "[Success] When: the type is a plain named type ; Should: keep the package name only",
			typ:  reflect.TypeFor[tripState](), want: "sagas.tripState",
		},
		{
			name: "[Success] When: the type has type arguments ; Should: strip each import path and space the list",
			typ:  reflect.TypeFor[Step[otherState, None]](), want: "sagas.Step[sagas.otherState, sagas.None]",
		},
		{
			name: "[Success] When: a type argument is generic ; Should: strip the nested paths too",
			typ:  reflect.TypeFor[Step[otherState, typed[tripState, Hotel]]](),
			want: "sagas.Step[sagas.otherState, sagas.typed[sagas.tripState, sagas.Hotel]]",
		},
		{
			name: "[Success] When: the type is a pointer to a generic type ; Should: keep the star",
			typ:  reflect.TypeFor[*Pivot[tripState, Hotel]](), want: "*sagas.Pivot[sagas.tripState, sagas.Hotel]",
		},
		{
			name: "[Success] When: the type is a slice and a map of generic types ; Should: strip inside both",
			typ:  reflect.TypeFor[map[string][]Step[tripState, None]](),
			want: "map[string][]sagas.Step[sagas.tripState, sagas.None]",
		},
		{
			name: "[Success] When: the type is predeclared ; Should: keep it as it is",
			typ:  reflect.TypeFor[string](), want: "string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, shortType(tt.typ))
		})
	}
}

func settingsFor(f Flow) *stepSettings {
	embed, _ := embedsStep(reflect.TypeOf(f))
	s := settingsOf(f, &embed)

	return &s
}

func Test_settingsOf(t *testing.T) {
	t.Parallel()

	step := act("a", okAction)
	step.Description, step.RetryPolicy, step.AttemptTimeout, step.FailurePolicy = "books", NoRetry(), time.Second, Halt

	pivot := paying("p", okAction)
	pivot.Description, pivot.RetryPolicy, pivot.AttemptTimeout = "charges", RetryPolicy{Attempts: 2}, time.Minute

	tests := []struct {
		name string
		flow Flow
		want stepSettings
	}{
		{
			name: "[Success] When: a Step sets every field ; Should: read each one and no pivot mark",
			flow: step,
			want: stepSettings{
				name: "a", description: "books", retry: NoRetry(), timeout: time.Second, failure: Halt,
			},
		},
		{
			name: "[Success] When: a Pivot sets every field ; Should: read each one and mark the pivot",
			flow: pivot,
			want: stepSettings{
				name: "p", description: "charges", retry: RetryPolicy{Attempts: 2}, timeout: time.Minute, pivot: true,
			},
		},
		{
			name: "[Success] When: the Step comes through an embedded struct ; Should: read its fields",
			flow: &nestedReserve{Name: "nested"},
			want: stepSettings{name: "nested"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embed, ok := embedsStep(reflect.TypeOf(tt.flow))
			require.True(t, ok)

			assert.Equal(t, tt.want, settingsOf(tt.flow, &embed))
		})
	}
}
