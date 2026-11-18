package sagas

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const stepA = `step "a"`

func Test_Step(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		steps     []Option
		wantNames []string
		wantErr   error
	}{
		{
			name:      "[Success] When: two steps are declared ; Should: keep declaration order",
			steps:     []Option{Step("hotel", okAction), Step("flight", okAction)},
			wantNames: []string{"hotel", "flight"},
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
			for _, n := range s.steps {
				got = append(got, n.name)
			}

			assert.Equal(t, tt.wantNames, got)
		})
	}
}

func Test_Compensate(t *testing.T) {
	t.Parallel()

	undo := func(context.Context, *tripState) error { return nil }

	tests := []struct {
		name       string
		opts       []StepOption
		wantPolicy CompensationFailurePolicy
		wantErr    error
	}{
		{
			name:       "[Success] When: a compensation is given ; Should: allow a failure policy on the step",
			opts:       []StepOption{Compensate(undo), OnCompensationFailure(CompensationHalt)},
			wantPolicy: CompensationHalt,
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
		name    string
		opts    []StepOption
		want    time.Duration
		wantErr error
	}{
		{
			name: "[Success] When: a timeout is given ; Should: store it",
			opts: []StepOption{Timeout(time.Second)}, want: time.Second,
		},
		{
			name: "[Success] When: the option is absent ; Should: store zero",
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

			s, err := oneStep(tt.opts...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, s.steps[0].timeout)
		})
	}
}

func Test_Retry(t *testing.T) {
	t.Parallel()

	given := RetryPolicy{Attempts: 5, Initial: time.Second, Max: time.Minute, Multiplier: 1.5, NoJitter: true}

	tests := []struct {
		name    string
		opts    []StepOption
		want    RetryPolicy
		wantErr error
	}{
		{
			name: "[Success] When: a policy is given ; Should: store it as given",
			opts: []StepOption{Retry(given)}, want: given,
		},
		{
			name: "[Success] When: the option is absent ; Should: store the zero policy",
		},
		{
			name: "[Success] When: the multiplier is exactly one ; Should: accept it",
			opts: []StepOption{Retry(RetryPolicy{Multiplier: 1})}, want: RetryPolicy{Multiplier: 1},
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

			s, err := oneStep(tt.opts...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, s.steps[0].retry)
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

			errs := checkStep[tripState](&d.steps[0])
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
