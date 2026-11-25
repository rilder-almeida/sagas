package sagas

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tripState struct {
	Hotel string
	Rate  float64
}

type otherState struct {
	Car string
}

const (
	tripName = "trip"
	sagaTrip = `saga "trip"`
	noReach  = "neither before nor after"
	quotedB  = `"b"`

	nilOptionText = "nil Option"
)

var errAlwaysFails = errors.New("sagas: test: the action always fails")

func okAction(context.Context, *tripState) error { return nil }

func alwaysFails(context.Context, *tripState) error { return errAlwaysFails }

func twoSecondCall(ctx context.Context, _ *tripState) error {
	select {
	case <-time.After(2 * time.Second):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func callOnce(tb testing.TB, call callFunc[tripState]) outcome[tripState] {
	tb.Helper()

	return call(context.Background(), &callMeta{execution: infoExecution}, baseOf(tb, tripState{}))
}

func okPredicate(*tripState) bool { return true }

func okMiddleware() MiddlewareFunc {
	return func(ctx context.Context, _ CallInfo, call Call) error { return call(ctx) }
}

type pointerJournal struct{}

func (*pointerJournal) Append(context.Context, *Event) error { return nil }

func twice[T any](v T) []T { return []T{v, v} }

func oneStep() (*Saga[tripState], error) {
	return New[tripState](tripName, act("a", okAction))
}

func Test_New(t *testing.T) {
	t.Parallel()

	journal := NewMemoryJournal()
	a := act("a", okAction)

	tests := []struct {
		name         string
		saga         string
		flow         Flow
		opts         []Option
		wantErrs     []error
		wantContains []string
	}{
		{
			name: "[Success] When: the saga is valid ; Should: return a saga",
			saga: tripName, flow: a,
		},
		{
			name:         "[Fail] When: the saga name is empty ; Should: reject it naming the saga",
			flow:         a,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`saga ""`, "empty name"},
		},
		{
			name: "[Fail] When: the saga name is outside the allowed set ; Should: reject it naming the saga",
			saga: "a\tb", flow: a,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`saga "a\tb"`, badName},
		},
		{
			name: "[Fail] When: an Option is nil ; Should: reject it",
			saga: tripName, flow: a, opts: []Option{nil},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, nilOptionText},
		},
		{
			name: "[Fail] When: WithJournal is nil ; Should: reject it",
			saga: tripName, flow: a, opts: []Option{WithJournal(nil)},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, "WithJournal journal is nil"},
		},
		{
			name: "[Fail] When: WithMiddleware holds a nil entry ; Should: reject it",
			saga: tripName, flow: a, opts: []Option{WithMiddleware(okMiddleware(), nil)},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, "nil Middleware"},
		},
		{
			name: "[Fail] When: WithRecoveryTimeout is negative ; Should: reject it",
			saga: tripName, flow: a, opts: []Option{WithRecoveryTimeout(-time.Second)},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{sagaTrip, "WithRecoveryTimeout is negative"},
		},
		{
			name: "[Fail] When: WithJournal is given twice ; Should: reject it",
			saga: tripName, flow: a, opts: twice(WithJournal(journal)),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"WithJournal given more than once"},
		},
		{
			name: "[Fail] When: WithMiddleware is given twice ; Should: reject it",
			saga: tripName, flow: a, opts: twice(WithMiddleware(okMiddleware())),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"WithMiddleware given more than once"},
		},
		{
			name: "[Fail] When: WithRecoveryTimeout is given twice ; Should: reject it",
			saga: tripName, flow: a, opts: twice(WithRecoveryTimeout(time.Second)),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{"WithRecoveryTimeout given more than once"},
		},
		{
			name:         "[Fail] When: the flow is nil ; Should: return the Inspect error for a saga without steps",
			saga:         tripName,
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{noStepsText},
		},
		{
			name: "[Fail] When: a step name breaks the name rule ; Should: return the Inspect error naming the step",
			saga: tripName, flow: act("a b", okAction),
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`step "a b"`, badName},
		},
		{
			name: "[Fail] When: the flow holds a cycle ; Should: return the Inspect ErrCycle",
			saga: tripName, flow: Sequence(a, a),
			wantErrs:     []error{ErrCycle},
			wantContains: []string{quotedA, "form a cycle"},
		},
		{
			name: "[Fail] When: two steps share a name ; Should: return the Inspect ErrDuplicateStep",
			saga: tripName, flow: Parallel(a, act("a", okAction)),
			wantErrs:     []error{ErrDuplicateStep},
			wantContains: []string{`step "a": declared more than once`},
		},
		{
			name: "[Fail] When: the saga and a step are both bad ; Should: join both errors",
			flow: &idleStep{}, opts: []Option{nil},
			wantErrs:     []error{ErrInvalidStep},
			wantContains: []string{`saga ""`, nilOptionText, neitherText},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New[tripState](tt.saga, tt.flow, tt.opts...)
			_, inspectErr := Inspect[tripState](tt.flow)

			requireErrs(t, err, tt.wantErrs, nil)
			requireText(t, err, tt.wantContains, nil)
			assert.Contains(t, errText(err), errText(inspectErr))
			assert.Equal(t, err == nil, s != nil)
		})
	}
}

func Test_New_stepSettings(t *testing.T) {
	t.Parallel()

	given := RetryPolicy{Attempts: 5, Initial: time.Second, Max: 2 * time.Second, Multiplier: 1.5, NoJitter: true}

	tests := []struct {
		name         string
		settings     Step
		execute      func(context.Context, *tripState) error
		changed      Step
		wantAttempts int
		wantElapsed  time.Duration
		wantErr      error
		wantErrTries int
	}{
		{
			name:     "[Fail] When: RetryPolicy is NoRetry and Execute fails ; Should: run it once",
			settings: Step{RetryPolicy: NoRetry()}, execute: alwaysFails,
			wantAttempts: 1, wantErr: errAlwaysFails, wantErrTries: 1,
		},
		{
			name:     "[Fail] When: RetryPolicy gives two attempts ; Should: stop an always failing step after two",
			settings: Step{RetryPolicy: RetryPolicy{Attempts: 2, NoJitter: true}}, execute: alwaysFails,
			wantAttempts: 2, wantElapsed: 200 * time.Millisecond, wantErr: errAlwaysFails, wantErrTries: 2,
		},
		{
			name:     "[Fail] When: RetryPolicy sets every field ; Should: wait its exact delays",
			settings: Step{RetryPolicy: given}, execute: alwaysFails,
			wantAttempts: 5, wantElapsed: 6500 * time.Millisecond, wantErr: errAlwaysFails, wantErrTries: 5,
		},
		{
			name:         "[Fail] When: RetryPolicy is zero apart from NoJitter ; Should: wait the default delays",
			settings:     Step{RetryPolicy: RetryPolicy{NoJitter: true}},
			execute:      alwaysFails,
			wantAttempts: DefaultAttempts,
			wantElapsed:  600 * time.Millisecond,
			wantErr:      errAlwaysFails,
			wantErrTries: DefaultAttempts,
		},
		{
			name:         "[Fail] When: the multiplier is exactly one ; Should: wait a constant delay",
			settings:     Step{RetryPolicy: RetryPolicy{Multiplier: 1, NoJitter: true}},
			execute:      alwaysFails,
			wantAttempts: DefaultAttempts,
			wantElapsed:  400 * time.Millisecond,
			wantErr:      errAlwaysFails,
			wantErrTries: DefaultAttempts,
		},
		{
			name:     "[Fail] When: AttemptTimeout is shorter than Execute ; Should: end it with DeadlineExceeded",
			settings: Step{RetryPolicy: NoRetry(), AttemptTimeout: time.Second}, execute: twoSecondCall,
			wantAttempts: 1, wantElapsed: time.Second, wantErr: context.DeadlineExceeded, wantErrTries: 1,
		},
		{
			name:     "[Success] When: AttemptTimeout is longer than Execute ; Should: let it finish",
			settings: Step{AttemptTimeout: time.Minute}, execute: twoSecondCall,
			wantAttempts: 1, wantElapsed: 2 * time.Second,
		},
		{
			name:     "[Success] When: AttemptTimeout is zero ; Should: set no limit",
			settings: Step{}, execute: twoSecondCall,
			wantAttempts: 1, wantElapsed: 2 * time.Second,
		},
		{
			name:     "[Fail] When: the step value changes after New ; Should: run with the settings New copied",
			settings: Step{RetryPolicy: NoRetry()}, execute: alwaysFails,
			changed: Step{
				Name: "x", RetryPolicy: RetryPolicy{Attempts: 9, NoJitter: true}, AttemptTimeout: time.Nanosecond,
			},
			wantAttempts: 1, wantErr: errAlwaysFails, wantErrTries: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				step := act("a", tt.execute)
				step.RetryPolicy, step.AttemptTimeout = tt.settings.RetryPolicy, tt.settings.AttemptTimeout

				journal := newScriptJournal(nil)

				s, err := New[tripState](tripName, step, WithJournal(journal))
				require.NoError(t, err)

				step.Step = tt.changed

				start := time.Now()
				runErr := s.Run(t.Context(), &tripState{})
				elapsed := time.Since(start)

				res := journal.results(t)[0]

				require.ErrorIs(t, runErr, tt.wantErr)
				assert.Equal(t, tt.wantErr == nil, runErr == nil)
				require.Len(t, res.Steps, 1)
				assert.Equal(t, "a", res.Steps[0].Name)
				events := storedEvents(t, journal.MemoryJournal, res.Execution)
				assert.Equal(t, tt.wantAttempts, outcomeOf(events, "a").Attempt)
				assert.Equal(t, tt.wantElapsed, elapsed)

				errTries := 0
				if stepErr, ok := errors.AsType[*Error](runErr); ok {
					errTries = stepErr.Attempts
				}

				assert.Equal(t, tt.wantErrTries, errTries)
			})
		})
	}
}

func requireText(t *testing.T, err error, contains, absent []string) {
	t.Helper()

	for _, want := range contains {
		require.ErrorContains(t, err, want)
	}

	for _, no := range absent {
		require.NotContains(t, errText(err), no)
	}
}

func Test_WithJournal(t *testing.T) {
	t.Parallel()

	var nilJournal *pointerJournal

	tests := []struct {
		name        string
		opts        func(j *scriptJournal) []Option
		wantAppends int
		wantErr     error
	}{
		{
			name:        "[Success] When: a journal is given ; Should: append every event of the run to it",
			opts:        func(j *scriptJournal) []Option { return []Option{WithJournal(j)} },
			wantAppends: 4,
		},
		{
			name: "[Success] When: no journal is given ; Should: run on a journal of its own",
			opts: func(*scriptJournal) []Option { return nil },
		},
		{
			name:    "[Fail] When: the journal is nil ; Should: reject it",
			opts:    func(*scriptJournal) []Option { return []Option{WithJournal(nil)} },
			wantErr: ErrInvalidStep,
		},
		{
			name:    "[Fail] When: the journal holds a nil pointer ; Should: reject it",
			opts:    func(*scriptJournal) []Option { return []Option{WithJournal(nilJournal)} },
			wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			j := newScriptJournal(nil)

			s, err := New[tripState](tripName, act("a", okAction), tt.opts(j)...)
			require.ErrorIs(t, err, tt.wantErr)

			if s != nil {
				runErr := s.Run(t.Context(), &tripState{})
				require.NoError(t, runErr)
			}

			assert.Len(t, j.ctxs, tt.wantAppends)
		})
	}
}

type graceJournal struct {
	waited time.Duration
}

func (j *graceJournal) Append(ctx context.Context, e *Event) error {
	if e.Kind != EventStepUnknown {
		return nil
	}

	start := time.Now()

	<-ctx.Done()

	j.waited = max(j.waited, time.Since(start))

	return context.Cause(ctx)
}

func Test_WithRecoveryTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     []Option
		wantWait time.Duration
		wantErr  error
	}{
		{
			name: "[Success] When: a timeout is given ; Should: keep the journal context alive for it",
			opts: []Option{WithRecoveryTimeout(time.Second)}, wantWait: time.Second,
		},
		{
			name:     "[Success] When: the option is absent ; Should: use the default",
			wantWait: DefaultRecoveryTimeout,
		},
		{
			name: "[Success] When: the timeout is zero ; Should: use the default",
			opts: []Option{WithRecoveryTimeout(0)}, wantWait: DefaultRecoveryTimeout,
		},
		{
			name: "[Fail] When: the timeout is negative ; Should: reject it",
			opts: []Option{WithRecoveryTimeout(-1)}, wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				j := &graceJournal{}
				waits := once("a", func(ctx context.Context, _ *tripState) error {
					<-ctx.Done()

					return ctx.Err()
				})

				s, err := New[tripState](tripName, waits, append([]Option{WithJournal(j)}, tt.opts...)...)
				require.ErrorIs(t, err, tt.wantErr)

				if s != nil {
					ctx, cancel := context.WithCancel(t.Context())
					time.AfterFunc(pause, cancel)

					runErr := s.Run(ctx, &tripState{})
					require.ErrorIs(t, runErr, ErrGraceEnded)
				}

				assert.Equal(t, tt.wantWait, j.waited)
			})
		})
	}
}

func middlewaresOf(rec *recorder, names []string) []Middleware {
	mws := make([]Middleware, 0, len(names))

	for _, n := range names {
		mws = append(mws, phased(rec, n))
	}

	return mws
}

const (
	outerMW   = "outer"
	closeOut  = "<outer"
	closeMW   = "<mw"
	mwExecute = "mw>pay:execute"
)

func phased(r *recorder, name string) MiddlewareFunc {
	return func(ctx context.Context, info CallInfo, call Call) error {
		r.add(name + ">" + info.Step + ":" + string(info.Phase))

		err := call(ctx)

		r.add("<" + name)

		return err
	}
}

func overwrite(mws []Middleware) {
	for i := range mws {
		mws[i] = okMiddleware()
	}
}

func Test_WithMiddleware(t *testing.T) {
	t.Parallel()

	both := []string{
		"outer>a:execute", "inner>a:execute", "<inner", closeOut,
		"outer>b:execute", "inner>b:execute", "<inner", closeOut,
	}

	tests := []struct {
		name           string
		names          []string
		extra          []Middleware
		calls          int
		changeAfterNew bool
		pivot          bool
		wantTrace      []string
		wantErr        error
	}{
		{
			name:  "[Success] When: the second step is the pivot ; Should: give the middleware the phase pivot for it",
			names: []string{outerMW}, calls: 1, pivot: true,
			wantTrace: []string{"outer>a:execute", closeOut, "outer>b:pivot", closeOut},
		},
		{
			name:  "[Success] When: two middlewares are given in one call ; Should: wrap each step call in order",
			names: []string{outerMW, "inner"}, calls: 1, wantTrace: both,
		},
		{
			name:  "[Success] When: the caller changes its slice after New ; Should: run the original ones",
			names: []string{outerMW, "inner"}, calls: 1, changeAfterNew: true, wantTrace: both,
		},
		{
			name:  "[Fail] When: an entry is nil ; Should: reject it",
			calls: 1, extra: []Middleware{nil}, wantErr: ErrInvalidStep,
		},
		{
			name:  "[Fail] When: an entry is a nil MiddlewareFunc ; Should: reject it",
			calls: 1, extra: []Middleware{MiddlewareFunc(nil)}, wantErr: ErrInvalidStep,
		},
		{
			name:  "[Fail] When: an entry is a nil pointer middleware ; Should: reject it",
			calls: 1, extra: []Middleware{(*tagMiddleware)(nil)}, wantErr: ErrInvalidStep,
		},
		{
			name:  "[Fail] When: WithMiddleware is called twice ; Should: reject it",
			names: []string{outerMW}, calls: 2, wantErr: ErrInvalidStep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recorder{}

			mws := append(middlewaresOf(rec, tt.names), tt.extra...)

			var second Flow = act("b", okAction)
			if tt.pivot {
				second = paying("b", okAction)
			}

			opts := slices.Repeat([]Option{WithMiddleware(mws...)}, tt.calls)

			s, err := New[tripState](tripName, Sequence(act("a", okAction), second), opts...)
			require.ErrorIs(t, err, tt.wantErr)

			if tt.changeAfterNew {
				overwrite(mws)
			}

			if s != nil {
				runErr := s.Run(t.Context(), &tripState{})
				require.NoError(t, runErr)
			}

			assert.Equal(t, tt.wantTrace, rec.snapshot())
		})
	}
}

func Test_stepNode_buildCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		pivot            bool
		undo             bool
		wrap             bool
		wantCompensation bool
		wantTrace        []string
	}{
		{
			name:      "[Success] When: a step without a compensation ; Should: run it as execute, no compensation",
			wantTrace: []string{mwExecute, "do", closeMW},
		},
		{
			name:      "[Success] When: a pivot step ; Should: run the action as pivot and build no compensation",
			pivot:     true,
			wantTrace: []string{"mw>pay:pivot", "do", closeMW},
		},
		{
			name:             "[Success] When: a step with a compensation ; Should: wrap it as compensate",
			undo:             true,
			wantCompensation: true,
			wantTrace:        []string{mwExecute, "do", closeMW, "mw>pay:compensate", "undo", closeMW},
		},
		{
			name:      "[Success] When: a step has its own Wrap ; Should: run it inside the saga middleware",
			wrap:      true,
			wantTrace: []string{mwExecute, "own>pay:execute", "do", "<own", closeMW},
		},
		{
			name:             "[Success] When: a step with a compensation has its own Wrap ; Should: wrap both calls",
			undo:             true,
			wrap:             true,
			wantCompensation: true,
			wantTrace: []string{
				mwExecute, "own>pay:execute", "do", "<own", closeMW,
				"mw>pay:compensate", "own>pay:compensate", "undo", "<own", closeMW,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recorder{}
			methods := &stepMethods[tripState]{call: func(context.Context, *tripState) error {
				rec.add("do")

				return nil
			}}

			if tt.undo {
				methods.compensate = func(context.Context, *tripState) error {
					rec.add("undo")

					return nil
				}
			}

			if tt.wrap {
				methods.wrap = phased(rec, "own")
			}

			report := &StepReport{Name: payStep, Pivot: tt.pivot, RetryPolicy: NoRetry()}

			var n stepNode[tripState]

			n.buildCalls(tripName, report, methods, []Middleware{phased(rec, "mw")})

			require.Equal(t, outcomeSuccess, callOnce(t, n.action).kind)
			require.Equal(t, tt.wantCompensation, n.compensation != nil)

			if n.compensation != nil {
				require.Equal(t, outcomeSuccess, callOnce(t, n.compensation).kind)
			}

			assert.Equal(t, tt.wantTrace, rec.snapshot())
		})
	}
}

func Test_checkRepeats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		given      map[string]int
		nilOptions int
		want       []string
	}{
		{
			name:  "[Success] When: each option is given once ; Should: find nothing",
			given: map[string]int{"WithJournal": 1, "WithMiddleware": 1},
			want:  nil,
		},
		{
			name:       "[Fail] When: an option is nil ; Should: report a nil Option",
			given:      map[string]int{},
			nilOptions: 2,
			want:       []string{nilOptionText},
		},
		{
			name:  "[Fail] When: two options repeat ; Should: report each one in name order",
			given: map[string]int{"WithRecoveryTimeout": 2, "WithJournal": 3, "WithMiddleware": 1},
			want:  []string{"WithJournal given more than once", "WithRecoveryTimeout given more than once"},
		},
		{
			name:       "[Fail] When: an option is nil and another repeats ; Should: report the nil Option first",
			given:      map[string]int{"WithJournal": 2},
			nilOptions: 1,
			want:       []string{nilOptionText, "WithJournal given more than once"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, checkRepeats(tt.given, tt.nilOptions))
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

type fuzzGraph struct {
	deps  [][]int
	pivot []bool
	comp  []bool
}

func decodeFuzzGraph(b []byte) fuzzGraph {
	at := func(i int) byte {
		if i < len(b) {
			return b[i]
		}

		return 0
	}

	n := 1 + int(at(0))%6
	g := fuzzGraph{deps: make([][]int, n), pivot: make([]bool, n), comp: make([]bool, n)}

	for i := range n {
		for j := range n {
			if at(1+i)>>j&1 == 1 {
				g.deps[i] = append(g.deps[i], j)
			}
		}

		g.pivot[i] = at(1+n+i)&1 == 1
		g.comp[i] = at(1+n+i)&2 == 2
	}

	return g
}

func (g fuzzGraph) items() []Flow {
	steps := make([]Flow, len(g.deps))

	for i := range g.deps {
		name := fmt.Sprintf("s%d", i)

		switch {
		case g.pivot[i] && g.comp[i]:
			steps[i] = &pivotUndoStep{Name: name, pay: okAction, undo: okAction}
		case g.pivot[i]:
			steps[i] = paying(name, okAction)
		case g.comp[i]:
			steps[i] = undoing(name, okAction, okAction)
		default:
			steps[i] = act(name, okAction)
		}
	}

	items := slices.Clone(steps)

	for i, deps := range g.deps {
		for _, j := range deps {
			items = append(items, Sequence(steps[j], steps[i]))
		}
	}

	return items
}

func (g fuzzGraph) reaches(from, to int) bool {
	seen := make([]bool, len(g.deps))

	var walk func(int) bool

	walk = func(at int) bool {
		for _, next := range g.deps[at] {
			if next == to {
				return true
			}

			if !seen[next] {
				seen[next] = true

				if walk(next) {
					return true
				}
			}
		}

		return false
	}

	return walk(from)
}

func (g fuzzGraph) cyclic() bool {
	for i := range g.deps {
		if g.reaches(i, i) {
			return true
		}
	}

	return false
}

func (g fuzzGraph) pivotBroken() bool {
	var pivots []int

	for i, isPivot := range g.pivot {
		if isPivot {
			pivots = append(pivots, i)
		}
	}

	switch {
	case len(pivots) > 1:
		return true
	case len(pivots) == 0:
		return false
	case g.comp[pivots[0]]:
		return true
	case g.cyclic():
		return false
	}

	return g.reachBroken(pivots[0])
}

func (g fuzzGraph) reachBroken(p int) bool {
	for i := range g.deps {
		ancestor, descendant := g.reaches(p, i), g.reaches(i, p)

		if i != p && !ancestor && !descendant {
			return true
		}

		if descendant && g.comp[i] {
			return true
		}
	}

	return false
}

func FuzzNew(f *testing.F) {
	f.Add([]byte{4, 0, 0, 0, 7, 8, 2, 2, 2, 1, 0})
	f.Add([]byte{1, 2, 1, 0, 0})
	f.Add([]byte{1, 0, 0, 1, 0})
	f.Add([]byte{1, 0, 1, 1, 2})

	f.Fuzz(func(t *testing.T, b []byte) {
		g := decodeFuzzGraph(b)

		_, err := New[tripState](tripName, Parallel(g.items()...))

		cyclic := g.cyclic()
		assert.Equal(t, cyclic || g.pivotBroken(), err != nil, "error: %v", err)

		if cyclic {
			require.ErrorIs(t, err, ErrCycle)
		}
	})
}
