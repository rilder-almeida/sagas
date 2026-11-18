package sagas

import (
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

			got := topoOrder(tt.parents)
			assert.Equal(t, tt.wantOrder, got.order)
			assert.Equal(t, tt.wantCycle, got.cycle)
		})
	}
}

func Test_indexSteps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		names     []string
		wantIndex map[string]int
		wantErrs  int
	}{
		{
			name:      "[Success] When: every name is unique ; Should: index each by position",
			names:     []string{"a", "b"},
			wantIndex: map[string]int{"a": 0, "b": 1},
		},
		{
			name:      "[Fail] When: a name repeats ; Should: keep the first index and report each extra one",
			names:     []string{"a", "b", "a", "a"},
			wantIndex: map[string]int{"a": 0, "b": 1},
			wantErrs:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			defs := make([]stepDef, 0, len(tt.names))
			for _, n := range tt.names {
				defs = append(defs, stepDef{name: n})
			}

			index, errs := indexSteps(defs)
			assert.Equal(t, tt.wantIndex, index)
			require.Len(t, errs, tt.wantErrs)

			for _, err := range errs {
				require.ErrorIs(t, err, ErrDuplicateStep)
			}
		})
	}
}

func Test_link(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		defs        []stepDef
		wantParents [][]int
		wantErrs    int
	}{
		{
			name:        "[Success] When: steps name known dependencies ; Should: link each to its parents in order",
			defs:        []stepDef{{name: "a"}, {name: "b"}, {name: "c", after: []string{"b", "a"}}},
			wantParents: [][]int{{}, {}, {1, 0}},
		},
		{
			name:        "[Success] When: a name repeats in one list ; Should: add one edge",
			defs:        []stepDef{{name: "a"}, {name: "b", after: []string{"a", "a"}}},
			wantParents: [][]int{{}, {0}},
		},
		{
			name:        "[Fail] When: a dependency is unknown ; Should: skip its edge and report it",
			defs:        []stepDef{{name: "a"}, {name: "b", after: []string{"ghost", "a"}}},
			wantParents: [][]int{{}, {0}},
			wantErrs:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			index, indexErrs := indexSteps(tt.defs)
			require.Empty(t, indexErrs)

			parents, errs := link(tt.defs, index)
			assert.Equal(t, tt.wantParents, parents)
			require.Len(t, errs, tt.wantErrs)

			for _, err := range errs {
				require.ErrorIs(t, err, ErrUnknownDependency)
			}
		})
	}
}
