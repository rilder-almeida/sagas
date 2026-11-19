package sagas

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const stepA = `step "a"`

func Test_Step(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		steps      []Option
		wantNames  []string
		wantHotels []string
		wantErr    error
	}{
		{
			name:       "[Success] When: two steps are declared ; Should: keep declaration order",
			steps:      []Option{Step("hotel", okAction), Step("flight", okAction)},
			wantNames:  []string{"hotel", "flight"},
			wantHotels: []string{"", ""},
		},
		{
			name:       "[Success] When: the action writes the state ; Should: carry the written state in the outcome",
			steps:      []Option{Step("hotel", bookHotel)},
			wantNames:  []string{"hotel"},
			wantHotels: []string{hotelBooked},
		},
		{
			name:    "[Fail] When: the name is empty ; Should: reject it",
			steps:   []Option{Step("", okAction)},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: the action is nil ; Should: reject it",
			steps:   []Option{Step[tripState]("hotel", nil)},
			wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New[tripState]("trip", tt.steps...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			got := make([]string, 0, len(s.steps))
			hotels := make([]string, 0, len(s.steps))

			for _, n := range s.steps {
				got = append(got, n.name)
				hotels = append(hotels, hotelOf(t, callOnce(t, n.action)))
			}

			assert.Equal(t, tt.wantNames, got)
			assert.Equal(t, tt.wantHotels, hotels)
		})
	}
}

func Test_After(t *testing.T) {
	t.Parallel()

	node := func(name string, after ...string) Option {
		if len(after) == 0 {
			return Step(name, okAction)
		}

		return Step(name, okAction, After(after...))
	}

	tests := []struct {
		name         string
		steps        []Option
		wantOrder    []int
		wantParents  [][]int
		wantChildren [][]int
		wantContains string
		wantErr      error
	}{
		{
			name:         "[Success] When: the steps form a chain ; Should: order them along it",
			steps:        []Option{node("c", "b"), node("b", "a"), node("a")},
			wantOrder:    []int{2, 1, 0},
			wantParents:  [][]int{{1}, {2}, {}},
			wantChildren: [][]int{{}, {0}, {1}},
		},
		{
			name:         "[Success] When: the steps form a diamond ; Should: list both parents and both children",
			steps:        []Option{node("a"), node("b", "a"), node("c", "a"), node("d", "b", "c")},
			wantOrder:    []int{0, 1, 2, 3},
			wantParents:  [][]int{{}, {0}, {0}, {1, 2}},
			wantChildren: [][]int{{1, 2}, {3}, {3}, {}},
		},
		{
			name:         "[Success] When: two steps are independent ; Should: keep declaration order",
			steps:        []Option{node("b"), node("a")},
			wantOrder:    []int{0, 1},
			wantParents:  [][]int{{}, {}},
			wantChildren: [][]int{{}, {}},
		},
		{
			name:         "[Fail] When: a step waits for an unknown step ; Should: return ErrUnknownDependency",
			steps:        []Option{node("a", "ghost")},
			wantErr:      ErrUnknownDependency,
			wantContains: `After names unknown step "ghost"`,
		},
		{
			name:         "[Fail] When: After is given twice ; Should: reject it",
			steps:        []Option{Step("a", okAction, twice(After())...)},
			wantErr:      ErrInvalidStep,
			wantContains: "After given more than once",
		},
		{
			name:         "[Fail] When: a name repeats inside one list ; Should: reject it naming the name",
			steps:        []Option{node("a"), node("b", "a", "a")},
			wantErr:      ErrInvalidStep,
			wantContains: `After lists "a" more than once`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New[tripState]("trip", tt.steps...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, tt.wantContains)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantOrder, s.order)

			for i, n := range s.steps {
				assert.Equal(t, tt.wantParents[i], n.parents, "parents of step %d", i)
				assert.Equal(t, tt.wantChildren[i], n.children, "children of step %d", i)
			}
		})
	}
}

func Test_Pivot(t *testing.T) {
	t.Parallel()

	undo := func(context.Context, *tripState) error { return nil }

	tests := []struct {
		name      string
		steps     []Option
		wantPivot int
		wantZones []Zone
		wantErr   error

		wantContains string
	}{
		{
			name: "[Success] When: the pivot pays after three bookings and a receipt follows ; Should: set each zone",
			steps: []Option{
				Step("hotel", okAction, Compensate(undo)),
				Step("flight", okAction, Compensate(undo)),
				Step("car", okAction, Compensate(undo), If(func(*tripState) bool { return true })),
				Step("pay", okAction, Pivot(), After("hotel", "flight", "car")),
				Step("receipt", okAction, After("pay")),
			},
			wantPivot: 3,
			wantZones: []Zone{ZoneCompensable, ZoneCompensable, ZoneCompensable, ZonePivot, ZoneForwardOnly},
		},
		{
			name:      "[Success] When: no step is the pivot ; Should: store -1 and make every step compensable",
			steps:     []Option{Step("a", okAction), Step("b", okAction, After("a"))},
			wantPivot: -1,
			wantZones: []Zone{ZoneCompensable, ZoneCompensable},
		},
		{
			name:         "[Fail] When: Pivot is given twice on one step ; Should: reject it",
			steps:        []Option{Step("a", okAction, twice(Pivot())...)},
			wantErr:      ErrInvalidStep,
			wantContains: "Pivot given more than once",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New[tripState]("trip", tt.steps...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.ErrorContains(t, err, tt.wantContains)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantPivot, s.pivot)

			got := make([]Zone, 0, len(s.steps))
			for _, n := range s.steps {
				got = append(got, n.zone)
			}

			assert.Equal(t, tt.wantZones, got)
		})
	}
}

func Test_Compensate(t *testing.T) {
	t.Parallel()

	undo := func(_ context.Context, s *tripState) error {
		s.Hotel = hotelUndone

		return nil
	}

	tests := []struct {
		name        string
		opts        []StepOption
		wantPolicy  CompensationFailurePolicy
		wantUndoing string
		wantErr     error
	}{
		{
			name:       "[Success] When: a compensation is given ; Should: run it and keep the policy",
			opts:       []StepOption{Compensate(undo), OnCompensationFailure(CompensationHalt)},
			wantPolicy: CompensationHalt, wantUndoing: hotelUndone,
		},
		{
			name:       "[Success] When: no compensation is given ; Should: store no compensation",
			wantPolicy: CompensationBlock,
		},
		{
			name:    "[Fail] When: the function is for another state type ; Should: reject it",
			opts:    []StepOption{Compensate(func(context.Context, *otherState) error { return nil })},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: the function is nil ; Should: reject it",
			opts:    []StepOption{Compensate[tripState](nil)},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: Compensate is given twice ; Should: reject it",
			opts:    twice(Compensate(undo)),
			wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := oneStep(tt.opts...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantPolicy, s.steps[0].policy)

			if tt.wantUndoing == "" {
				assert.Nil(t, s.steps[0].compensation)

				return
			}

			require.NotNil(t, s.steps[0].compensation)
			assert.Equal(t, tt.wantUndoing, hotelOf(t, callOnce(t, s.steps[0].compensation)))
		})
	}
}

func Test_If(t *testing.T) {
	t.Parallel()

	pred := func(s *tripState) bool { return s.Hotel == "x" }

	tests := []struct {
		name    string
		opts    []StepOption
		wantSet bool
		wantErr error
	}{
		{
			name:    "[Success] When: a predicate is given ; Should: store it",
			opts:    []StepOption{If(pred)},
			wantSet: true,
		},
		{
			name: "[Success] When: no predicate is given ; Should: store none",
		},
		{
			name:    "[Fail] When: the predicate is for another state type ; Should: reject it",
			opts:    []StepOption{If(func(*otherState) bool { return true })},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: the predicate is nil ; Should: reject it",
			opts:    []StepOption{If[tripState](nil)},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: If is given twice ; Should: reject it",
			opts:    twice(If(pred)),
			wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := oneStep(tt.opts...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			if !tt.wantSet {
				assert.Nil(t, s.steps[0].pred)

				return
			}

			require.NotNil(t, s.steps[0].pred)
			assert.True(t, s.steps[0].pred(&tripState{Hotel: "x"}))
			assert.False(t, s.steps[0].pred(&tripState{}))
		})
	}
}

func Test_Timeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		opts          []StepOption
		want          time.Duration
		wantActionErr error
		wantErr       error
	}{
		{
			name: "[Success] When: a timeout shorter than the action is given ; Should: end it with DeadlineExceeded",
			opts: []StepOption{Timeout(time.Second)}, want: time.Second, wantActionErr: context.DeadlineExceeded,
		},
		{
			name: "[Success] When: a timeout longer than the action is given ; Should: let the action finish",
			opts: []StepOption{Timeout(time.Minute)}, want: time.Minute,
		},
		{
			name: "[Success] When: the option is absent ; Should: store zero and let the action finish",
		},
		{
			name:    "[Fail] When: the timeout is negative ; Should: reject it",
			opts:    []StepOption{Timeout(-time.Second)},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: Timeout is given twice ; Should: reject it",
			opts:    twice(Timeout(time.Second)),
			wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				s, err := New[tripState]("trip",
					Step("a", twoSecondCall, append([]StepOption{Compensate(twoSecondCall)}, tt.opts...)...))
				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)

					return
				}

				require.NoError(t, err)
				assert.Equal(t, tt.want, s.steps[0].timeout)

				action := callOnce(t, s.steps[0].action)
				if tt.wantActionErr == nil {
					assert.Equal(t, outcomeSuccess, action.kind)
				} else {
					require.ErrorIs(t, action.err, tt.wantActionErr)
				}

				assert.Equal(t, outcomeSuccess, callOnce(t, s.steps[0].compensation).kind)
			})
		})
	}
}

func Test_Retry(t *testing.T) {
	t.Parallel()

	given := RetryPolicy{Attempts: 5, Initial: time.Second, Max: time.Minute, Multiplier: 1.5, NoJitter: true}

	tests := []struct {
		name         string
		opts         []StepOption
		want         RetryPolicy
		wantAttempts int
		wantErr      error
	}{
		{
			name: "[Success] When: a policy is given ; Should: store it as given",
			opts: []StepOption{Retry(given)}, want: given, wantAttempts: 5,
		},
		{
			name:         "[Success] When: the option is absent ; Should: store zero and make the default attempts",
			wantAttempts: DefaultAttempts,
		},
		{
			name: "[Success] When: two attempts are given ; Should: stop an always failing action after two",
			opts: []StepOption{Retry(RetryPolicy{Attempts: 2})}, want: RetryPolicy{Attempts: 2}, wantAttempts: 2,
		},
		{
			name: "[Success] When: the zero policy is given ; Should: make the default attempts",
			opts: []StepOption{Retry(RetryPolicy{})}, wantAttempts: DefaultAttempts,
		},
		{
			name: "[Success] When: the multiplier is exactly one ; Should: accept it",
			opts: []StepOption{Retry(RetryPolicy{Multiplier: 1})}, want: RetryPolicy{Multiplier: 1},
			wantAttempts: DefaultAttempts,
		},
		{
			name:    "[Fail] When: Attempts is negative ; Should: reject it",
			opts:    []StepOption{Retry(RetryPolicy{Attempts: -1})},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: Multiplier is below one ; Should: reject it",
			opts:    []StepOption{Retry(RetryPolicy{Multiplier: 0.5})},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: Retry is given twice ; Should: reject it",
			opts:    twice(Retry(given)),
			wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				s, err := New[tripState]("trip", Step("a", alwaysFails, tt.opts...))
				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)

					return
				}

				require.NoError(t, err)
				assert.Equal(t, tt.want, s.steps[0].retry)
				assert.Equal(t, tt.wantAttempts, callOnce(t, s.steps[0].action).attempts)
			})
		})
	}
}

func Test_checkRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy RetryPolicy
		want   []string
	}{
		{
			name: "[Success] When: the policy is the zero value ; Should: report nothing",
		},
		{
			name:   "[Fail] When: Attempts is negative ; Should: name Retry.Attempts",
			policy: RetryPolicy{Attempts: -1},
			want:   []string{"Retry.Attempts is negative"},
		},
		{
			name:   "[Fail] When: Initial is negative ; Should: name Retry.Initial",
			policy: RetryPolicy{Initial: -1},
			want:   []string{"Retry.Initial is negative"},
		},
		{
			name:   "[Fail] When: Max is negative ; Should: name Retry.Max",
			policy: RetryPolicy{Max: -1},
			want:   []string{"Retry.Max is negative"},
		},
		{
			name:   "[Fail] When: Multiplier is negative ; Should: name Retry.Multiplier",
			policy: RetryPolicy{Multiplier: -1},
			want:   []string{"Retry.Multiplier is negative"},
		},
		{
			name:   "[Fail] When: Multiplier is between zero and one ; Should: name Retry.Multiplier",
			policy: RetryPolicy{Multiplier: 0.5},
			want:   []string{"Retry.Multiplier is below 1"},
		},
		{
			name:   "[Fail] When: the default Max is below Initial ; Should: name Retry.Max",
			policy: RetryPolicy{Initial: time.Minute},
			want:   []string{"Retry.Max is below Retry.Initial after defaults"},
		},
		{
			name:   "[Fail] When: two fields are bad ; Should: report one text each",
			policy: RetryPolicy{Attempts: -1, Multiplier: -1},
			want:   []string{"Retry.Attempts is negative", "Retry.Multiplier is negative"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, checkRetry(tt.policy))
		})
	}
}

func Test_OnCompensationFailure(t *testing.T) {
	t.Parallel()

	undo := func(context.Context, *tripState) error { return nil }

	tests := []struct {
		name    string
		opts    []StepOption
		want    CompensationFailurePolicy
		wantErr error
	}{
		{
			name: "[Success] When: the option is absent ; Should: store block",
			want: CompensationBlock,
		},
		{
			name: "[Success] When: the policy is empty ; Should: store block",
			opts: []StepOption{Compensate(undo), OnCompensationFailure("")}, want: CompensationBlock,
		},
		{
			name: "[Success] When: the policy is continue ; Should: store continue",
			opts: []StepOption{Compensate(undo), OnCompensationFailure(CompensationContinue)},
			want: CompensationContinue,
		},
		{
			name: "[Success] When: the policy is halt ; Should: store halt",
			opts: []StepOption{Compensate(undo), OnCompensationFailure(CompensationHalt)},
			want: CompensationHalt,
		},
		{
			name:    "[Fail] When: the policy is unknown ; Should: reject it",
			opts:    []StepOption{Compensate(undo), OnCompensationFailure("stop")},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: the step has no Compensate ; Should: reject it",
			opts:    []StepOption{OnCompensationFailure(CompensationBlock)},
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: OnCompensationFailure is given twice ; Should: reject it",
			opts:    append([]StepOption{Compensate(undo)}, twice(OnCompensationFailure(CompensationHalt))...),
			wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := oneStep(tt.opts...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, s.steps[0].policy)
		})
	}
}

func Test_checkStep(t *testing.T) {
	t.Parallel()

	undo := func(context.Context, *tripState) error { return nil }
	otherUndo := func(context.Context, *otherState) error { return nil }
	pred := func(*tripState) bool { return true }

	tests := []struct {
		name         string
		step         Option
		wantContains []string
	}{
		{
			name: "[Success] When: the step is valid ; Should: report nothing",
			step: Step("a", okAction),
		},
		{
			name:         "[Fail] When: the name is empty ; Should: report it",
			step:         Step("", okAction),
			wantContains: []string{"empty name"},
		},
		{
			name:         "[Fail] When: the name holds a newline ; Should: report the control character",
			step:         Step("a\nb", okAction),
			wantContains: []string{`step "a\nb"`, "name holds a control character"},
		},
		{
			name:         "[Fail] When: the action is nil ; Should: report it naming the step",
			step:         Step[tripState]("a", nil),
			wantContains: []string{stepA, "action is nil"},
		},
		{
			name:         "[Fail] When: the action is for another state type ; Should: report it",
			step:         Step("a", func(context.Context, *otherState) error { return nil }),
			wantContains: []string{stepA, "action is for another state type"},
		},
		{
			name:         "[Fail] When: Compensate is for another state type ; Should: report it",
			step:         Step("a", okAction, Compensate(otherUndo)),
			wantContains: []string{stepA, "Compensate function is for another state type"},
		},
		{
			name:         "[Fail] When: If is for another state type ; Should: report it",
			step:         Step("a", okAction, If(func(*otherState) bool { return true })),
			wantContains: []string{stepA, "If function is for another state type"},
		},
		{
			name:         "[Fail] When: Compensate is nil ; Should: report it",
			step:         Step("a", okAction, Compensate[tripState](nil)),
			wantContains: []string{stepA, "Compensate function is nil"},
		},
		{
			name:         "[Fail] When: If is nil ; Should: report it",
			step:         Step("a", okAction, If[tripState](nil)),
			wantContains: []string{stepA, "If function is nil"},
		},
		{
			name:         "[Fail] When: a StepOption is nil ; Should: report it",
			step:         Step("a", okAction, nil),
			wantContains: []string{stepA, "nil StepOption"},
		},
		{
			name:         "[Fail] When: Timeout is negative ; Should: report it",
			step:         Step("a", okAction, Timeout(-time.Second)),
			wantContains: []string{stepA, "Timeout is negative"},
		},
		{
			name:         "[Fail] When: Retry has a negative field ; Should: name the field",
			step:         Step("a", okAction, Retry(RetryPolicy{Attempts: -1})),
			wantContains: []string{stepA, "Retry.Attempts"},
		},
		{
			name:         "[Fail] When: Retry.Multiplier is below one ; Should: report it",
			step:         Step("a", okAction, Retry(RetryPolicy{Multiplier: 0.5})),
			wantContains: []string{"Retry.Multiplier", "below 1"},
		},
		{
			name:         "[Fail] When: Retry.Initial exceeds the default Max ; Should: report it",
			step:         Step("a", okAction, Retry(RetryPolicy{Initial: time.Minute})),
			wantContains: []string{"Retry.Max", "below Retry.Initial"},
		},
		{
			name:         "[Fail] When: the policy is unknown ; Should: report it",
			step:         Step("a", okAction, Compensate(undo), OnCompensationFailure("stop")),
			wantContains: []string{stepA, "\"stop\""},
		},
		{
			name:         "[Fail] When: OnCompensationFailure has no Compensate ; Should: report it",
			step:         Step("a", okAction, OnCompensationFailure(CompensationHalt)),
			wantContains: []string{stepA, "OnCompensationFailure without Compensate"},
		},
		{
			name:         "[Fail] When: Compensate is given twice ; Should: report it",
			step:         Step("a", okAction, twice(Compensate(undo))...),
			wantContains: []string{"Compensate given more than once"},
		},
		{
			name:         "[Fail] When: If is given twice ; Should: report it",
			step:         Step("a", okAction, twice(If(pred))...),
			wantContains: []string{"If given more than once"},
		},
		{
			name:         "[Fail] When: Retry is given twice ; Should: report it",
			step:         Step("a", okAction, twice(Retry(RetryPolicy{}))...),
			wantContains: []string{"Retry given more than once"},
		},
		{
			name:         "[Fail] When: Timeout is given twice ; Should: report it",
			step:         Step("a", okAction, twice(Timeout(time.Second))...),
			wantContains: []string{"Timeout given more than once"},
		},
		{
			name:         "[Fail] When: Pivot is given twice ; Should: report it",
			step:         Step("a", okAction, twice(Pivot())...),
			wantContains: []string{"Pivot given more than once"},
		},
		{
			name:         "[Fail] When: After is given twice ; Should: report it",
			step:         Step("a", okAction, After("x"), After("y")),
			wantContains: []string{"After given more than once"},
		},
		{
			name:         "[Fail] When: a name repeats inside one After list ; Should: report the name",
			step:         Step("a", okAction, After("x", "y", "x")),
			wantContains: []string{stepA, `After lists "x" more than once`},
		},
		{
			name: "[Fail] When: OnCompensationFailure is given twice ; Should: report it",
			step: Step(
				"a",
				okAction,
				append([]StepOption{Compensate(undo)}, twice(OnCompensationFailure(CompensationHalt))...)...),
			wantContains: []string{"OnCompensationFailure given more than once"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := &definition{given: map[string]int{}}
			tt.step(d)
			require.Len(t, d.steps, 1)

			_, errs := checkStep[tripState](&d.steps[0])
			if len(tt.wantContains) == 0 {
				assert.Empty(t, errs)

				return
			}

			err := errors.Join(errs...)
			require.ErrorIs(t, err, ErrInvalidStep)

			for _, want := range tt.wantContains {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}
