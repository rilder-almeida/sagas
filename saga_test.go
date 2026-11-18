package sagas

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tripState struct {
	Hotel string
}

type otherState struct {
	Car string
}

const (
	quotedB  = `"b"`
	sagaTrip = `saga "trip"`
	quotedA  = `"a"`
	noReach  = "neither before nor after"
)

var errNilSaga = errors.New("sagas: test: New returned a nil saga without an error")

func okAction(context.Context, *tripState) error { return nil }

func okPredicate(*tripState) bool { return true }

func okMiddleware(next Handler) Handler { return next }

func okMiddleware2(next Handler) Handler { return next }

type pointerJournal struct{}

func (*pointerJournal) Append(context.Context, *Event) error { return nil }

func twice[T any](v T) []T { return []T{v, v} }

func buildTrip(opts ...Option) error {
	s, err := New[tripState]("trip", opts...)
	if err != nil {
		return err
	}

	if s == nil {
		return errNilSaga
	}

	return nil
}

func oneStep(opts ...StepOption) (*Saga[tripState], error) {
	return New[tripState]("trip", Step("a", okAction, opts...))
}

type newCase struct {
	name         string
	build        func() error
	wantErrs     []error
	wantNoErrs   []error
	wantContains []string
	wantAbsent   []string
}

func newGraphCases() []newCase {
	step := func(opts ...StepOption) Option { return Step("a", okAction, opts...) }

	return []newCase{
		{
			name:         "[Fail] When: two steps share a name ; Should: return ErrDuplicateStep naming it",
			build:        func() error { return buildTrip(twice(step())...) },
			wantErrs:     []error{ErrDuplicateStep},
			wantContains: []string{`step "a": declared more than once`},
		},
		{
			name: "[Fail] When: two steps wait for each other ; Should: return ErrCycle naming both",
			build: func() error {
				return buildTrip(Step("a", okAction, After("b")), Step("b", okAction, After("a")))
			},
			wantErrs:     []error{ErrCycle},
			wantContains: []string{`saga "trip"`, quotedA, quotedB, "form a cycle"},
		},
		{
			name: "[Fail] When: a step only waits behind a cycle ; Should: name the cycle steps and not that step",
			build: func() error {
				return buildTrip(
					Step("a", okAction, After("b")),
					Step("b", okAction, After("a")),
					Step("c", okAction, After("a")),
				)
			},
			wantErrs:     []error{ErrCycle},
			wantContains: []string{quotedA, quotedB},
			wantAbsent:   []string{`"c"`},
		},
		{
			name:         "[Fail] When: a step waits for itself ; Should: return ErrCycle naming it",
			build:        func() error { return buildTrip(Step("a", okAction, After("a"))) },
			wantErrs:     []error{ErrCycle},
			wantContains: []string{`"a"`},
		},
		{
			name: "[Fail] When: one step has an unknown dependency and two others form a cycle ; Should: return both",
			build: func() error {
				return buildTrip(
					Step("a", okAction, After("ghost")),
					Step("b", okAction, After("c")),
					Step("c", okAction, After("b")),
				)
			},
			wantErrs:     []error{ErrUnknownDependency, ErrCycle},
			wantContains: []string{`"ghost"`, quotedB, `"c"`},
		},
		{
			name: "[Fail] When: two steps are pivots ; Should: reject it naming both",
			build: func() error {
				return buildTrip(Step("a", okAction, Pivot()), Step("b", okAction, Pivot(), After("a")))
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, "more than one Pivot", `"a"`, quotedB},
		},
		{
			name: "[Fail] When: a cycle exists and two steps are pivots ; Should: report the cycle and the pivot count",
			build: func() error {
				return buildTrip(
					Step("a", okAction, After("b")),
					Step("b", okAction, After("a")),
					Step("p", okAction, Pivot()),
					Step("q", okAction, Pivot()),
				)
			},
			wantErrs:     []error{ErrCycle, ErrInvalidStep},
			wantContains: []string{"more than one Pivot"},
			wantAbsent:   []string{noReach},
		},
		{
			name: "[Fail] When: two pivots and a step parallel to both exist ; Should: report only the pivot count",
			build: func() error {
				return buildTrip(
					Step("a", okAction, Pivot()),
					Step("b", okAction, Pivot(), After("a")),
					Step("c", okAction),
				)
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"more than one Pivot"},
			wantAbsent:   []string{noReach},
		},
		{
			name: "[Fail] When: a dependency is unknown and a step is parallel to the pivot ; Should: report only that",
			build: func() error {
				return buildTrip(
					Step("a", okAction, Pivot()),
					Step("b", okAction, After("ghost")),
					Step("c", okAction),
				)
			},
			wantErrs:     []error{ErrUnknownDependency},
			wantNoErrs:   []error{ErrInvalidStep},
			wantContains: []string{`step "b"`, `After names unknown step "ghost"`},
			wantAbsent:   []string{noReach},
		},
		{
			name: "[Fail] When: a step is parallel to the pivot ; Should: reject it naming the step",
			build: func() error {
				return buildTrip(Step("a", okAction, Pivot()), Step("b", okAction))
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "b"`, "neither before nor after the pivot"},
		},
		{
			name: "[Fail] When: the pivot has Compensate ; Should: reject it",
			build: func() error {
				return buildTrip(Step("a", okAction, Pivot(), Compensate(okAction)))
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "a"`, "pivot has Compensate"},
		},
		{
			name:         "[Fail] When: the pivot has If ; Should: reject it",
			build:        func() error { return buildTrip(Step("a", okAction, Pivot(), If(okPredicate))) },
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "a"`, "pivot has If"},
		},
		{
			name: "[Fail] When: a step after the pivot has Compensate ; Should: reject it naming the step",
			build: func() error {
				return buildTrip(Step("a", okAction, Pivot()), Step("b", okAction, After("a"), Compensate(okAction)))
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "b"`, "after the pivot has Compensate"},
		},
		{
			name: "[Fail] When: a cycle and one pivot exist ; Should: report only the cycle",
			build: func() error {
				return buildTrip(
					Step("a", okAction, After("b")),
					Step("b", okAction, After("a")),
					Step("p", okAction, Pivot()),
				)
			},
			wantErrs:   []error{ErrCycle},
			wantNoErrs: []error{ErrInvalidStep},
			wantAbsent: []string{noReach},
		},
		{
			name: "[Fail] When: a cycle exists and the pivot has Compensate ; Should: report both errors",
			build: func() error {
				return buildTrip(
					Step("a", okAction, After("b")),
					Step("b", okAction, After("a")),
					Step("p", okAction, Pivot(), Compensate(okAction)),
				)
			},
			wantErrs:     []error{ErrCycle, ErrInvalidStep},
			wantContains: []string{"pivot has Compensate"},
		},
	}
}

func Test_New(t *testing.T) {
	t.Parallel()

	step := func(opts ...StepOption) Option { return Step("a", okAction, opts...) }
	journal := newMemoryJournal()

	tests := slices.Concat(newGraphCases(), []newCase{
		{
			name:  "[Success] When: the saga is valid ; Should: return a saga",
			build: func() error { return buildTrip(step()) },
		},
		{
			name: "[Fail] When: the saga name is empty ; Should: reject it naming the saga",
			build: func() error {
				_, err := New[tripState]("", step())

				return err
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`saga ""`, "empty name"},
		},
		{
			name: "[Fail] When: the saga has no steps ; Should: reject it",
			build: func() error {
				_, err := New[tripState]("trip")

				return err
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, "no steps"},
		},
		{
			name:         "[Fail] When: an Option is nil ; Should: reject it",
			build:        func() error { return buildTrip(step(), nil) },
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, "nil Option"},
		},
		{
			name:         "[Fail] When: a step is invalid ; Should: reject the saga naming the step",
			build:        func() error { return buildTrip(Step[tripState]("hotel", nil)) },
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"step \"hotel\"", "action is nil"},
		},
		{
			name:         "[Fail] When: WithJournal is nil ; Should: reject it",
			build:        func() error { return buildTrip(step(), WithJournal(nil)) },
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, "WithJournal journal is nil"},
		},
		{
			name:         "[Fail] When: WithMiddleware holds a nil entry ; Should: reject it",
			build:        func() error { return buildTrip(step(), WithMiddleware(okMiddleware, nil)) },
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, "nil Middleware"},
		},
		{
			name:         "[Fail] When: WithRecoveryTimeout is negative ; Should: reject it",
			build:        func() error { return buildTrip(step(), WithRecoveryTimeout(-time.Second)) },
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, "WithRecoveryTimeout is negative"},
		},
		{
			name: "[Fail] When: WithJournal is given twice ; Should: reject it",
			build: func() error {
				return buildTrip(slices.Concat([]Option{step()}, twice(WithJournal(journal)))...)
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"WithJournal given more than once"},
		},
		{
			name: "[Fail] When: WithMiddleware is given twice ; Should: reject it",
			build: func() error {
				return buildTrip(slices.Concat([]Option{step()}, twice(WithMiddleware(okMiddleware)))...)
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"WithMiddleware given more than once"},
		},
		{
			name: "[Fail] When: WithRecoveryTimeout is given twice ; Should: reject it",
			build: func() error {
				return buildTrip(slices.Concat([]Option{step()}, twice(WithRecoveryTimeout(time.Second)))...)
			},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"WithRecoveryTimeout given more than once"},
		},
		{
			name: "[Fail] When: the state type breaks the state rules ; Should: return ErrInvalidState",
			build: func() error {
				_, err := New[chanState]("trip", Step("a", func(context.Context, *chanState) error { return nil }))

				return err
			},
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"chanState"},
		},
		{
			name: "[Fail] When: a step and the state type are both bad ; Should: join both errors",
			build: func() error {
				_, err := New[chanState]("trip", Step[chanState]("a", nil))

				return err
			},
			wantErrs:     []error{ErrInvalidStep, ErrInvalidState},
			wantContains: []string{"action is nil", "chanState"},
		},
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.build()
			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErrs {
				require.ErrorIs(t, err, want)
			}

			for _, no := range tt.wantNoErrs {
				require.NotErrorIs(t, err, no)
			}

			requireText(t, err, tt.wantContains, tt.wantAbsent)
		})
	}
}

func requireText(t *testing.T, err error, contains, absent []string) {
	t.Helper()

	for _, want := range contains {
		require.ErrorContains(t, err, want)
	}

	for _, no := range absent {
		require.NotContains(t, err.Error(), no)
	}
}

func Test_WithJournal(t *testing.T) {
	t.Parallel()

	journal := newMemoryJournal()

	var nilJournal *pointerJournal

	tests := []struct {
		name        string
		opts        []Option
		wantJournal Journal
		wantErr     error
	}{
		{
			name: "[Success] When: a journal is given ; Should: store it",
			opts: []Option{WithJournal(journal)}, wantJournal: journal,
		},
		{
			name: "[Success] When: no journal is given ; Should: leave it nil",
		},
		{
			name: "[Fail] When: the journal is nil ; Should: reject it",
			opts: []Option{WithJournal(nil)}, wantErr: ErrInvalidStep,
		},
		{
			name: "[Fail] When: the journal holds a nil pointer ; Should: reject it",
			opts: []Option{WithJournal(nilJournal)}, wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New[tripState]("trip", append([]Option{Step("a", okAction)}, tt.opts...)...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantJournal, s.journal)
		})
	}
}

func Test_WithMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    []Option
		wantErr error
	}{
		{
			name: "[Success] When: several middlewares are given in one call ; Should: accept them",
			opts: []Option{WithMiddleware(okMiddleware, okMiddleware2)},
		},
		{
			name: "[Fail] When: an entry is nil ; Should: reject it",
			opts: []Option{WithMiddleware(nil)}, wantErr: ErrInvalidStep,
		},
		{
			name: "[Fail] When: WithMiddleware is called twice ; Should: reject it",
			opts: twice(WithMiddleware(okMiddleware)), wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := buildTrip(append([]Option{Step("a", okAction)}, tt.opts...)...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

func Test_WithRecoveryTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    []Option
		want    time.Duration
		wantErr error
	}{
		{
			name: "[Success] When: a timeout is given ; Should: store it",
			opts: []Option{WithRecoveryTimeout(time.Second)}, want: time.Second,
		},
		{
			name: "[Success] When: the option is absent ; Should: use the default",
			want: DefaultRecoveryTimeout,
		},
		{
			name: "[Success] When: the timeout is zero ; Should: use the default",
			opts: []Option{WithRecoveryTimeout(0)}, want: DefaultRecoveryTimeout,
		},
		{
			name: "[Fail] When: the timeout is negative ; Should: reject it",
			opts: []Option{WithRecoveryTimeout(-1)}, wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New[tripState]("trip", append([]Option{Step("a", okAction)}, tt.opts...)...)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, s.recovery)
		})
	}
}

func Test_isNilValue(t *testing.T) {
	t.Parallel()

	var (
		nilPointer *int
		nilMap     map[string]int
		nilSlice   []int
		nilFunc    func()
		nilChan    chan int
		number     = 1
	)

	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{name: "[Success] When: the value is an untyped nil ; Should: report nil", value: nil, want: true},
		{name: "[Success] When: the value is a nil pointer ; Should: report nil", value: nilPointer, want: true},
		{name: "[Success] When: the value is a nil map ; Should: report nil", value: nilMap, want: true},
		{name: "[Success] When: the value is a nil slice ; Should: report nil", value: nilSlice, want: true},
		{name: "[Success] When: the value is a nil func ; Should: report nil", value: nilFunc, want: true},
		{name: "[Success] When: the value is a nil chan ; Should: report nil", value: nilChan, want: true},
		{name: "[Success] When: the value is a set pointer ; Should: report not nil", value: &number},
		{name: "[Success] When: the value is a set map ; Should: report not nil", value: map[string]int{}},
		{name: "[Success] When: the value is a set slice ; Should: report not nil", value: []int{}},
		{name: "[Success] When: the value is a set func ; Should: report not nil", value: func() {}},
		{name: "[Success] When: the value is a set chan ; Should: report not nil", value: make(chan int)},
		{name: "[Success] When: the value is an int ; Should: report not nil", value: number},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, isNilValue(tt.value))
		})
	}
}
