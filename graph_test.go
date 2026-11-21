package sagas

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_topoOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		parents   [][]int
		wantOrder []int
		wantCycle []int
	}{
		{
			name:      "[Success] When: there are no steps ; Should: return an empty order",
			wantOrder: []int{},
		},
		{
			name:      "[Success] When: the steps form a chain ; Should: order them along it",
			parents:   [][]int{{1}, {2}, {}},
			wantOrder: []int{2, 1, 0},
		},
		{
			name:      "[Success] When: the steps form a diamond ; Should: put the join last",
			parents:   [][]int{{}, {0}, {0}, {1, 2}},
			wantOrder: []int{0, 1, 2, 3},
		},
		{
			name:      "[Success] When: steps are ready together ; Should: break the tie by index",
			parents:   [][]int{{}, {}, {}, {0, 1, 2}},
			wantOrder: []int{0, 1, 2, 3},
		},
		{
			name:      "[Success] When: a step frees an index below a ready one ; Should: pick the lowest ready index",
			parents:   [][]int{{1}, {}, {}},
			wantOrder: []int{1, 0, 2},
		},
		{
			name:      "[Fail] When: two steps wait for each other ; Should: return both as the cycle",
			parents:   [][]int{{1}, {0}},
			wantOrder: []int{},
			wantCycle: []int{0, 1},
		},
		{
			name:      "[Fail] When: a step waits for itself ; Should: return it as the cycle",
			parents:   [][]int{{0}},
			wantOrder: []int{},
			wantCycle: []int{0},
		},
		{
			name:      "[Fail] When: a ready step feeds a cycle ; Should: leave the cycle out of the order",
			parents:   [][]int{{}, {0, 2}, {1}},
			wantOrder: []int{0},
			wantCycle: []int{1, 2},
		},
		{
			name:      "[Fail] When: a step only waits behind a cycle ; Should: include it in the cycle",
			parents:   [][]int{{}, {2}, {1}, {2}},
			wantOrder: []int{0},
			wantCycle: []int{1, 2, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := topoOrder(tt.parents, childrenOf(tt.parents))
			assert.Equal(t, tt.wantOrder, got.order)
			assert.Equal(t, tt.wantCycle, got.cycle)
		})
	}
}

func Test_indexSteps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		flow         Flow
		wantErrs     int
		wantContains string
		wantHint     bool
	}{
		{
			name: "[Success] When: every name is unique ; Should: report nothing",
			flow: Sequence(act("a", okAction), &Reserve{}, &Reserve{Name: "b"}),
		},
		{
			name:         "[Fail] When: a set name repeats ; Should: report each extra one",
			flow:         Parallel(act("a", okAction), act("b", okAction), act("a", okAction), act("a", okAction)),
			wantErrs:     2,
			wantContains: `step "a": declared more than once`,
		},
		{
			name:         "[Fail] When: two steps of one type take the type name ; Should: say to set Name on each",
			flow:         Parallel(&Reserve{}, &Reserve{}),
			wantErrs:     1,
			wantContains: "set Name on each step of type *sagas.Reserve",
			wantHint:     true,
		},
		{
			name:         "[Fail] When: two instances of one generic type take the type name ; Should: name both types",
			flow:         Parallel(&Notify[int]{}, &Notify[string]{}),
			wantErrs:     1,
			wantContains: "set Name on each step of types *sagas.Notify[int] and *sagas.Notify[string]",
			wantHint:     true,
		},
		{
			name:         "[Fail] When: a set name equals the type name of another step ; Should: not blame the type",
			flow:         Parallel(&Reserve{}, act(reserveName, okAction)),
			wantErrs:     1,
			wantContains: `step "reserve": declared more than once`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := resolve(tt.flow)

			named := make([]namedStep, 0, len(r.flows))
			for _, f := range r.flows {
				named = append(named, nameStep(f))
			}

			errs := indexSteps(named)
			require.Len(t, errs, tt.wantErrs)

			for _, err := range errs {
				require.ErrorIs(t, err, ErrDuplicateStep)
				require.ErrorContains(t, err, tt.wantContains)
				assert.Equal(t, tt.wantHint, strings.Contains(err.Error(), setName))
			}
		})
	}
}

func Test_zones(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		children [][]int
		pivot    int
		want     []Zone
	}{
		{
			name:     "[Success] When: there is no pivot ; Should: make every step compensable",
			children: [][]int{{1}, {}},
			pivot:    -1,
			want:     []Zone{ZoneCompensable, ZoneCompensable},
		},
		{
			name:     "[Success] When: the pivot ends a chain ; Should: leave nothing forward-only",
			children: [][]int{{1}, {2}, {}},
			pivot:    2,
			want:     []Zone{ZoneCompensable, ZoneCompensable, ZonePivot},
		},
		{
			name:     "[Success] When: steps follow the pivot ; Should: make its descendants forward-only",
			children: [][]int{{1}, {2}, {3}, {}},
			pivot:    1,
			want:     []Zone{ZoneCompensable, ZonePivot, ZoneForwardOnly, ZoneForwardOnly},
		},
		{
			name:     "[Success] When: the graph is a diamond around the pivot ; Should: split before and after",
			children: [][]int{{1, 2}, {3}, {3}, {4}, {}},
			pivot:    3,
			want:     []Zone{ZoneCompensable, ZoneCompensable, ZoneCompensable, ZonePivot, ZoneForwardOnly},
		},
		{
			name:     "[Success] When: a descendant is reached by two paths ; Should: mark it once as forward-only",
			children: [][]int{{1, 2}, {3}, {3}, {}},
			pivot:    0,
			want:     []Zone{ZonePivot, ZoneForwardOnly, ZoneForwardOnly, ZoneForwardOnly},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, zones(tt.children, tt.pivot))
		})
	}
}
