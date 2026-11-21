package sagas

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reportOf(name string, after ...string) StepReport {
	return StepReport{Name: name, Execute: true, After: after}
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
		{
			name:        "[Success] When: empty groups sit in the sequence ; Should: skip them",
			flow:        Sequence(Parallel(), a, Sequence(), b),
			wantReports: []StepReport{reportOf("a"), reportOf("b", "a")},
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
		wantSteps   []Flow
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
			wantSteps:   []Flow{a},
			wantParents: [][]int{{}},
		},
		{
			name:        "[Success] When: a step follows two steps ; Should: link it to both in flow order",
			flow:        Sequence(Parallel(b, a), c),
			wantSteps:   []Flow{b, a, c},
			wantParents: [][]int{{}, {}, {0, 1}},
		},
		{
			name:        "[Success] When: a step waits for itself ; Should: link it to itself",
			flow:        Sequence(a, a),
			wantSteps:   []Flow{a},
			wantParents: [][]int{{0}},
		},
		{
			name:        "[Fail] When: a Sequence item is nil ; Should: report its position",
			flow:        Sequence(a, nil),
			wantSteps:   []Flow{a},
			wantParents: [][]int{{}},
			wantErr:     "Sequence item at index 1 is nil",
		},
		{
			name: "[Success] When: the flow is a nil Group ; Should: find no step",
			flow: nilGroup,
		},
		{
			name:        "[Fail] When: a Parallel item is a nil Group ; Should: report its position",
			flow:        Parallel(a, nilGroup),
			wantSteps:   []Flow{a},
			wantParents: [][]int{{}},
			wantErr:     "Parallel item at index 1 is nil",
		},
		{
			name:        "[Fail] When: a nested Parallel item is a typed nil pointer ; Should: report its position",
			flow:        Sequence(a, Parallel(b, typedNil)),
			wantSteps:   []Flow{a, b},
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
