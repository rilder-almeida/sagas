package sagas

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type doer struct {
	do func(context.Context, *tripState) error
}

func (d doer) Execute(ctx context.Context, s *tripState) error { return d.do(ctx, s) }

type undoer struct {
	undo func(context.Context, *tripState) error
}

func (u undoer) Compensate(ctx context.Context, s *tripState) error { return u.undo(ctx, s) }

type checker struct{ pred func(*tripState) bool }

func (c checker) Enabled(s *tripState) bool { return c.pred(s) }

type payer struct {
	pay func(context.Context, *tripState) error
}

func (p payer) Pivot(ctx context.Context, s *tripState) error { return p.pay(ctx, s) }

type wrapper struct{ wrap MiddlewareFunc }

func (w wrapper) Wrap(ctx context.Context, info CallInfo, call Call) error {
	return w.wrap(ctx, info, call)
}

type actStep struct {
	Step
	doer
}

type undoStep struct {
	Step
	doer
	undoer
}

type ifStep struct {
	Step
	doer
	checker
}

type pivotStep struct {
	Step
	payer
}

type pivotUndoStep struct {
	Step
	payer
	undoer
}

type pivotIfStep struct {
	Step
	payer
	checker
}

type fullStep struct {
	Step
	doer
	undoer
	checker
	wrapper
}

func act(name string, do func(context.Context, *tripState) error) *actStep {
	return &actStep{Name: name, do: do}
}

func undoing(name string, do, undo func(context.Context, *tripState) error) *undoStep {
	return &undoStep{Name: name, do: do, undo: undo}
}

func gated(name string, do func(context.Context, *tripState) error, pred func(*tripState) bool) *ifStep {
	return &ifStep{Name: name, do: do, pred: pred}
}

func paying(name string, pay func(context.Context, *tripState) error) *pivotStep {
	return &pivotStep{Name: name, pay: pay}
}

func once(name string, do func(context.Context, *tripState) error) *actStep {
	s := act(name, do)
	s.Retry = NoRetry()

	return s
}

func Test_checkName(t *testing.T) {
	t.Parallel()

	const badRule = `name may hold only ASCII letters, digits, "-", "_" and "." ` +
		"and must start with a letter or a digit"

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "[Success] When: the name is a plain word ; Should: accept it", input: "pay"},
		{name: "[Success] When: the name holds a dash ; Should: accept it", input: "book-trip"},
		{name: "[Success] When: the name holds an underscore ; Should: accept it", input: "hotel_v2"},
		{name: "[Success] When: the name holds a dot ; Should: accept it", input: "a.b"},
		{name: "[Success] When: the name starts with a digit ; Should: accept it", input: "9lives"},
		{
			name:  "[Fail] When: the name is empty ; Should: report an empty name",
			input: "", want: []string{"empty name"},
		},
		{name: "[Fail] When: the name starts with a dash ; Should: reject it", input: "-a", want: []string{badRule}},
		{name: "[Fail] When: the name starts with a dot ; Should: reject it", input: ".a", want: []string{badRule}},
		{name: "[Fail] When: the name holds a space ; Should: reject it", input: "a b", want: []string{badRule}},
		{name: "[Fail] When: the name holds a slash ; Should: reject it", input: "a/b", want: []string{badRule}},
		{
			name:  "[Fail] When: the name holds a non-ASCII letter ; Should: reject it",
			input: "café", want: []string{badRule},
		},
		{name: "[Fail] When: the name holds a double quote ; Should: reject it", input: `a"b`, want: []string{badRule}},
		{name: "[Fail] When: the name holds a hash ; Should: reject it", input: "a#b", want: []string{badRule}},
		{name: "[Fail] When: the name holds a backtick ; Should: reject it", input: "a`b", want: []string{badRule}},
		{name: "[Fail] When: the name holds a newline ; Should: reject it", input: "a\nb", want: []string{badRule}},
		{
			name:  "[Fail] When: the name holds a no-break space ; Should: reject it",
			input: "a b", want: []string{badRule},
		},
		{
			name:  "[Fail] When: the name holds invalid UTF-8 ; Should: reject it",
			input: "a\xffb", want: []string{badRule},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, checkName(tt.input))
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
			name:   "[Success] When: the policy is NoRetry ; Should: report nothing",
			policy: NoRetry(),
		},
		{
			name:   "[Fail] When: Attempts is negative ; Should: name Retry.Attempts",
			policy: RetryPolicy{Attempts: -1},
			want:   []string{negAttempts},
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
			want:   []string{negAttempts, "Retry.Multiplier is negative"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, checkRetry(tt.policy))
		})
	}
}

func Test_checkSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		report StepReport
		want   []string
	}{
		{
			name: "[Success] When: every setting is zero ; Should: report nothing",
		},
		{
			name: "[Success] When: the policy is continue on a step with Compensate ; Should: report nothing",
			report: StepReport{
				CompensationFailure: CompensationContinue,
				AttemptTimeout:      time.Second,
				Compensate:          true,
			},
		},
		{
			name:   "[Success] When: the policy is halt on a step with Compensate ; Should: report nothing",
			report: StepReport{CompensationFailure: CompensationHalt, Compensate: true},
		},
		{
			name:   "[Success] When: the policy is block on a step with Compensate ; Should: report nothing",
			report: StepReport{CompensationFailure: CompensationBlock, Compensate: true},
		},
		{
			name:   "[Fail] When: the policy is unknown ; Should: name it",
			report: StepReport{CompensationFailure: "stop", Compensate: true},
			want:   []string{`unknown compensation failure policy "stop"`},
		},
		{
			name:   "[Fail] When: the policy is set without Compensate ; Should: report it",
			report: StepReport{CompensationFailure: CompensationHalt},
			want:   []string{"CompensationFailure without Compensate"},
		},
		{
			name:   "[Fail] When: AttemptTimeout is negative ; Should: report it",
			report: StepReport{AttemptTimeout: -time.Second},
			want:   []string{"AttemptTimeout is negative"},
		},
		{
			name:   "[Fail] When: the retry policy has a negative field ; Should: name the field",
			report: StepReport{Retry: RetryPolicy{Attempts: -1}},
			want:   []string{negAttempts},
		},
		{
			name:   "[Fail] When: three settings are bad ; Should: report one text each",
			report: StepReport{CompensationFailure: "stop", AttemptTimeout: -1, Retry: RetryPolicy{Max: -1}},
			want: []string{
				"AttemptTimeout is negative", "Retry.Max is negative",
				`unknown compensation failure policy "stop"`, "CompensationFailure without Compensate",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, checkSettings(&tt.report))
		})
	}
}
