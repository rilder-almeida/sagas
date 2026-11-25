package sagas

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type doer struct {
	do func(context.Context, tripState) error
}

func (d doer) Execute(ctx context.Context, s tripState) error { return d.do(ctx, s) }

type maker struct {
	make func(context.Context, tripState) (Hotel, error)
}

func (m maker) Execute(ctx context.Context, s tripState) (Hotel, error) { return m.make(ctx, s) }

type rater struct {
	rate func(context.Context, tripState) (Rate, error)
}

func (r rater) Execute(ctx context.Context, s tripState) (Rate, error) { return r.rate(ctx, s) }

type undoer struct {
	undo func(context.Context, tripState) error
}

func (u undoer) Compensate(ctx context.Context, s tripState) error { return u.undo(ctx, s) }

type swapper struct {
	replace func(context.Context, tripState) (Hotel, error)
}

func (w swapper) Compensate(ctx context.Context, s tripState) (Hotel, error) {
	return w.replace(ctx, s)
}

type rateSwapper struct {
	replace func(context.Context, tripState) (Rate, error)
}

func (w rateSwapper) Compensate(ctx context.Context, s tripState) (Rate, error) {
	return w.replace(ctx, s)
}

type checker struct {
	pred func(context.Context, tripState) bool
}

func (c checker) Enabled(ctx context.Context, s tripState) bool { return c.pred(ctx, s) }

type wrapper struct{ wrap MiddlewareFunc }

func (w wrapper) Wrap(ctx context.Context, info CallInfo, call Call) error {
	return w.wrap(ctx, info, call)
}

type actStep struct {
	Step[tripState, None]
	doer
}

type makeStep struct {
	Step[tripState, Hotel]
	maker
}

type rateStep struct {
	Step[tripState, Rate]
	rater
}

type undoStep struct {
	Step[tripState, None]
	doer
	undoer
}

type undoMakeStep struct {
	Step[tripState, Hotel]
	maker
	undoer
}

type replaceStep struct {
	Step[tripState, Hotel]
	maker
	swapper
}

type replaceRateStep struct {
	Step[tripState, Rate]
	rater
	rateSwapper
}

type ifStep struct {
	Step[tripState, None]
	doer
	checker
}

type ifMakeStep struct {
	Step[tripState, Hotel]
	maker
	checker
}

type pivotStep struct {
	Pivot[tripState, None]
	doer
}

type pivotMakeStep struct {
	Pivot[tripState, Hotel]
	maker
}

type pivotRateStep struct {
	Pivot[tripState, Rate]
	rater
}

type pivotUndoStep struct {
	Pivot[tripState, None]
	doer
	undoer
}

type pivotIfStep struct {
	Pivot[tripState, None]
	doer
	checker
}

type fullStep struct {
	Step[tripState, None]
	doer
	undoer
	checker
	wrapper
}

func act(name string, do func(context.Context, tripState) error) *actStep {
	return &actStep{Name: name, do: do}
}

func producing(name string, do func(context.Context, tripState) (Hotel, error)) *makeStep {
	return &makeStep{Name: name, make: do}
}

func producingRate(name string, do func(context.Context, tripState) (Rate, error)) *rateStep {
	return &rateStep{Name: name, rate: do}
}

func undoing(name string, do, undo func(context.Context, tripState) error) *undoStep {
	return &undoStep{Name: name, do: do, undo: undo}
}

func undoingMade(
	name string, do func(context.Context, tripState) (Hotel, error), undo func(context.Context, tripState) error,
) *undoMakeStep {
	return &undoMakeStep{Name: name, make: do, undo: undo}
}

func gated(name string, do func(context.Context, tripState) error, pred func(context.Context, tripState) bool) *ifStep {
	return &ifStep{Name: name, do: do, pred: pred}
}

func paying(name string, pay func(context.Context, tripState) error) *pivotStep {
	return &pivotStep{Name: name, do: pay}
}

func payingMade(name string, pay func(context.Context, tripState) (Hotel, error)) *pivotMakeStep {
	return &pivotMakeStep{Name: name, make: pay}
}

func payingRate(name string, pay func(context.Context, tripState) (Rate, error)) *pivotRateStep {
	return &pivotRateStep{Name: name, rate: pay}
}

func replaceOnce(name string, do, undo func(context.Context, tripState) (Hotel, error)) *replaceStep {
	s := &replaceStep{Name: name, make: do, replace: undo}
	s.RetryPolicy = NoRetry()

	return s
}

func replaceRateOnce(name string, do, undo func(context.Context, tripState) (Rate, error)) *replaceRateStep {
	s := &replaceRateStep{Name: name, rate: do, replace: undo}
	s.RetryPolicy = NoRetry()

	return s
}

func once(name string, do func(context.Context, tripState) error) *actStep {
	s := act(name, do)
	s.RetryPolicy = NoRetry()

	return s
}

func Test_checkName(t *testing.T) {
	t.Parallel()

	const badRule = `name may hold only ASCII letters, digits, "-", "_" and "." ` +
		"and must start with a letter or a digit"

	tests := []struct {
		name  string
		input string
		want  ruleList
	}{
		{name: "[Success] When: the name is a plain word ; Should: accept it", input: "pay"},
		{name: "[Success] When: the name holds a dash ; Should: accept it", input: "book-trip"},
		{name: "[Success] When: the name holds an underscore ; Should: accept it", input: "hotel_v2"},
		{name: "[Success] When: the name holds a dot ; Should: accept it", input: "a.b"},
		{name: "[Success] When: the name starts with a digit ; Should: accept it", input: "9lives"},
		{
			name:  "[Fail] When: the name is empty ; Should: report an empty name",
			input: "", want: ruleList{"empty name"},
		},
		{name: "[Fail] When: the name starts with a dash ; Should: reject it", input: "-a", want: ruleList{badRule}},
		{name: "[Fail] When: the name starts with a dot ; Should: reject it", input: ".a", want: ruleList{badRule}},
		{name: "[Fail] When: the name holds a space ; Should: reject it", input: "a b", want: ruleList{badRule}},
		{name: "[Fail] When: the name holds a slash ; Should: reject it", input: "a/b", want: ruleList{badRule}},
		{
			name:  "[Fail] When: the name holds a non-ASCII letter ; Should: reject it",
			input: "café", want: ruleList{badRule},
		},
		{name: "[Fail] When: the name holds a double quote ; Should: reject it", input: `a"b`, want: ruleList{badRule}},
		{name: "[Fail] When: the name holds a hash ; Should: reject it", input: "a#b", want: ruleList{badRule}},
		{name: "[Fail] When: the name holds a backtick ; Should: reject it", input: "a`b", want: ruleList{badRule}},
		{name: "[Fail] When: the name holds a newline ; Should: reject it", input: "a\nb", want: ruleList{badRule}},
		{
			name:  "[Fail] When: the name holds a no-break space ; Should: reject it",
			input: "a b", want: ruleList{badRule},
		},
		{
			name:  "[Fail] When: the name holds invalid UTF-8 ; Should: reject it",
			input: "a\xffb", want: ruleList{badRule},
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
		want   ruleList
	}{
		{
			name: "[Success] When: the policy is the zero value ; Should: report nothing",
		},
		{
			name:   "[Success] When: the policy is NoRetry ; Should: report nothing",
			policy: NoRetry(),
		},
		{
			name:   "[Fail] When: Attempts is negative ; Should: name RetryPolicy.Attempts",
			policy: RetryPolicy{Attempts: -1},
			want:   ruleList{negAttempts},
		},
		{
			name:   "[Fail] When: Initial is negative ; Should: name RetryPolicy.Initial",
			policy: RetryPolicy{Initial: -1},
			want:   ruleList{"RetryPolicy.Initial is negative"},
		},
		{
			name:   "[Fail] When: Max is negative ; Should: name RetryPolicy.Max",
			policy: RetryPolicy{Max: -1},
			want:   ruleList{"RetryPolicy.Max is negative"},
		},
		{
			name:   "[Fail] When: Multiplier is negative ; Should: name RetryPolicy.Multiplier",
			policy: RetryPolicy{Multiplier: -1},
			want:   ruleList{"RetryPolicy.Multiplier is negative"},
		},
		{
			name:   "[Fail] When: Multiplier is between zero and one ; Should: name RetryPolicy.Multiplier",
			policy: RetryPolicy{Multiplier: 0.5},
			want:   ruleList{"RetryPolicy.Multiplier is below 1"},
		},
		{
			name:   "[Fail] When: the default Max is below Initial ; Should: name RetryPolicy.Max",
			policy: RetryPolicy{Initial: time.Minute},
			want:   ruleList{"RetryPolicy.Max is below RetryPolicy.Initial after defaults"},
		},
		{
			name:   "[Fail] When: two fields are bad ; Should: report one text each",
			policy: RetryPolicy{Attempts: -1, Multiplier: -1},
			want:   ruleList{negAttempts, "RetryPolicy.Multiplier is negative"},
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
		want   ruleList
	}{
		{
			name: "[Success] When: every setting is zero ; Should: report nothing",
		},
		{
			name: "[Success] When: the policy is continue on a step with Compensate ; Should: report nothing",
			report: StepReport{
				FailurePolicy:  Continue,
				AttemptTimeout: time.Second,
				Compensate:     true,
			},
		},
		{
			name:   "[Success] When: the policy is BlockAll on a step with Compensate ; Should: report nothing",
			report: StepReport{FailurePolicy: BlockAll, Compensate: true},
		},
		{
			name:   "[Success] When: the policy is block on a step with Compensate ; Should: report nothing",
			report: StepReport{FailurePolicy: Block, Compensate: true},
		},
		{
			name:   `[Success] When: FailurePolicy is "block-all" ; Should: no rule`,
			report: StepReport{FailurePolicy: "block-all", Compensate: true},
		},
		{
			name:   `[Fail] When: FailurePolicy is "halt" ; Should: unknown compensation failure policy "halt"`,
			report: StepReport{FailurePolicy: "halt", Compensate: true},
			want:   ruleList{`unknown compensation failure policy "halt"`},
		},
		{
			name:   "[Fail] When: the policy is unknown ; Should: name it",
			report: StepReport{FailurePolicy: "stop", Compensate: true},
			want:   ruleList{`unknown compensation failure policy "stop"`},
		},
		{
			name:   "[Fail] When: the policy is set without Compensate ; Should: report it",
			report: StepReport{FailurePolicy: BlockAll},
			want:   ruleList{"FailurePolicy without Compensate"},
		},
		{
			name:   "[Fail] When: AttemptTimeout is negative ; Should: report it",
			report: StepReport{AttemptTimeout: -time.Second},
			want:   ruleList{"AttemptTimeout is negative"},
		},
		{
			name:   "[Fail] When: the retry policy has a negative field ; Should: name the field",
			report: StepReport{RetryPolicy: RetryPolicy{Attempts: -1}},
			want:   ruleList{negAttempts},
		},
		{
			name:   "[Fail] When: three settings are bad ; Should: report one text each",
			report: StepReport{FailurePolicy: "stop", AttemptTimeout: -1, RetryPolicy: RetryPolicy{Max: -1}},
			want: ruleList{
				"AttemptTimeout is negative", "RetryPolicy.Max is negative",
				`unknown compensation failure policy "stop"`, "FailurePolicy without Compensate",
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
