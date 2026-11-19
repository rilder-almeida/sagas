package sagas

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type floatState struct {
	F float64
}

type graceKey struct{}

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type scriptJournal struct {
	errs   map[int]error
	failed map[int]bool
	ctxs   []context.Context
}

func (j *scriptJournal) Append(ctx context.Context, e *Event) error {
	j.ctxs = append(j.ctxs, ctx)

	err, scripted := j.errs[e.Seq]
	if !scripted || j.failed[e.Seq] {
		return nil
	}

	if j.failed == nil {
		j.failed = map[int]bool{}
	}

	j.failed[e.Seq] = true

	return err
}

func Test_WithExecutionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		ids           []string
		wantExecution string
		wantErrs      []error
		wantContains  []string
	}{
		{
			name:          "[Success] When: the option is given once ; Should: run with that execution ID",
			ids:           []string{"order-42"},
			wantExecution: "order-42",
		},
		{
			name:         "[Fail] When: the option is given twice ; Should: reject the run with ErrInvalidRun",
			ids:          []string{"order-42", "order-43"},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{"WithExecutionID given more than once"},
		},
		{
			name:         "[Fail] When: the ID breaks the name rule ; Should: reject the run with ErrInvalidRun",
			ids:          []string{"order:42"},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := make([]RunOption, 0, len(tt.ids))
			for _, id := range tt.ids {
				opts = append(opts, WithExecutionID(id))
			}

			cfg, err := checkRun(&tripState{}, opts)
			for _, want := range tt.wantErrs {
				require.ErrorIs(t, err, want)
			}

			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)
			}

			requireText(t, err, tt.wantContains, nil)
			assert.Equal(t, tt.wantExecution, cfg.execution)
		})
	}
}

func Test_checkRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		run           func() (runConfig, error)
		wantExecution *regexp.Regexp
		wantErrs      []error
		wantContains  []string
	}{
		{
			name:          "[Success] When: no option is given ; Should: draw a random 32-char hex execution",
			run:           func() (runConfig, error) { return checkRun(&tripState{}, nil) },
			wantExecution: hexID,
		},
		{
			name: "[Success] When: WithExecutionID is valid ; Should: keep the given execution",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, []RunOption{WithExecutionID("order-42")})
			},
			wantExecution: regexp.MustCompile(`^order-42$`),
		},
		{
			name:         "[Fail] When: an option is nil ; Should: return ErrInvalidRun",
			run:          func() (runConfig, error) { return checkRun(&tripState{}, []RunOption{nil}) },
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{"nil RunOption"},
		},
		{
			name: "[Fail] When: WithExecutionID is given twice ; Should: return ErrInvalidRun",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, twice(WithExecutionID("order-42")))
			},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{"WithExecutionID given more than once"},
		},
		{
			name: "[Fail] When: the execution ID is empty ; Should: return ErrInvalidRun",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, []RunOption{WithExecutionID("")})
			},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: checkName(""),
		},
		{
			name: "[Fail] When: the execution ID holds a colon ; Should: return ErrInvalidRun",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, []RunOption{WithExecutionID("order:42")})
			},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
		{
			name: "[Fail] When: the execution ID starts with a dash ; Should: return ErrInvalidRun",
			run: func() (runConfig, error) {
				return checkRun(&tripState{}, []RunOption{WithExecutionID("-x")})
			},
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
		{
			name:         "[Fail] When: s is nil ; Should: return ErrInvalidState",
			run:          func() (runConfig, error) { return checkRun[tripState](nil, nil) },
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"state is nil"},
		},
		{
			name:         "[Fail] When: the initial state holds NaN ; Should: return ErrInvalidState",
			run:          func() (runConfig, error) { return checkRun(&floatState{F: math.NaN()}, nil) },
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"F"},
		},
		{
			name:         "[Fail] When: an option and s are both bad ; Should: join both errors",
			run:          func() (runConfig, error) { return checkRun[tripState](nil, []RunOption{nil}) },
			wantErrs:     []error{ErrInvalidRun, ErrInvalidState},
			wantContains: []string{"nil RunOption", "state is nil"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := tt.run()
			if len(tt.wantErrs) > 0 {
				for _, want := range tt.wantErrs {
					require.ErrorIs(t, err, want)
				}

				requireText(t, err, tt.wantContains, nil)
				assert.Empty(t, cfg.execution)

				return
			}

			require.NoError(t, err)
			assert.Regexp(t, tt.wantExecution, cfg.execution)
			assert.Empty(t, checkName(cfg.execution))
		})
	}
}

func Test_newID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		calls int
	}{
		{name: "[Success] When: called twice ; Should: return two different 32-char hex IDs", calls: 2},
		{name: "[Success] When: called a hundred times ; Should: never repeat an ID", calls: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			seen := map[string]bool{}

			for range tt.calls {
				id := newID()
				assert.Regexp(t, hexID, id)
				assert.False(t, seen[id], id)

				seen[id] = true
			}

			assert.Len(t, seen, tt.calls)
		})
	}
}

func Test_runner_append(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		script          map[int]error
		appends         int
		wantSeq         int
		wantErrs        []error
		wantNoErrs      []error
		wantRejected    error
		wantJournalErrs int
	}{
		{
			name:    "[Success] When: the first event is appended ; Should: give it Seq 1 and the run's Writer",
			appends: 1,
			wantSeq: 1,
		},
		{
			name:    "[Success] When: two events are appended ; Should: number them 1 and 2",
			appends: 2,
			wantSeq: 2,
		},
		{
			name:            "[Fail] When: the journal returns a plain error ; Should: wrap ErrJournal and keep seq",
			script:          map[int]error{1: errAlwaysFails},
			appends:         1,
			wantErrs:        []error{ErrJournal, errAlwaysFails},
			wantNoErrs:      []error{ErrConcurrentWriter, ErrExecutionExists},
			wantJournalErrs: 1,
		},
		{
			name:         "[Fail] When: the journal rejects Seq 1 ; Should: store and return ErrExecutionExists as is",
			script:       map[int]error{1: fmt.Errorf("%w: exec-1", ErrExecutionExists)},
			appends:      1,
			wantErrs:     []error{ErrExecutionExists},
			wantNoErrs:   []error{ErrJournal},
			wantRejected: ErrExecutionExists,
		},
		{
			name:         "[Fail] When: the journal rejects Seq 2 ; Should: store and return ErrConcurrentWriter as is",
			script:       map[int]error{2: fmt.Errorf("%w: exec-1", ErrConcurrentWriter)},
			appends:      2,
			wantSeq:      1,
			wantErrs:     []error{ErrConcurrentWriter},
			wantNoErrs:   []error{ErrJournal},
			wantRejected: ErrConcurrentWriter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := oneStep()
			require.NoError(t, err)

			j := &scriptJournal{errs: tt.script}
			r := &runner[tripState]{
				saga: s, execution: "exec-1", writer: "writer-1", journal: j,
			}
			ctx := context.WithValue(t.Context(), graceKey{}, "grace")

			for i := range tt.appends {
				changed := []string{"Hotel"}
				err = r.append(ctx, &Event{Kind: EventStepSucceeded, Step: "a", Changed: changed})
				changed[0] = "mutated"

				if i < tt.appends-1 {
					require.NoError(t, err)
				}
			}

			requireAppendErr(t, err, tt.wantErrs, tt.wantNoErrs)
			assertAppended(t, r, j, tt.wantSeq, tt.appends)

			for _, got := range j.ctxs {
				assert.Equal(t, ctx, got)
			}

			assert.Len(t, r.journalErrs, tt.wantJournalErrs)

			if tt.wantRejected == nil {
				require.NoError(t, r.rejected)

				return
			}

			require.ErrorIs(t, r.rejected, tt.wantRejected)
		})
	}
}

func requireAppendErr(t *testing.T, err error, wantErrs, wantNoErrs []error) {
	t.Helper()

	if len(wantErrs) == 0 {
		require.NoError(t, err)

		return
	}

	for _, want := range wantErrs {
		require.ErrorIs(t, err, want)
	}

	for _, no := range wantNoErrs {
		require.NotErrorIs(t, err, no)
	}
}

func assertAppended(t *testing.T, r *runner[tripState], j *scriptJournal, wantSeq, appends int) {
	t.Helper()

	assert.Equal(t, wantSeq, r.seq)
	require.Len(t, r.events, wantSeq)
	require.Len(t, j.ctxs, appends)

	for i := range r.events {
		e := &r.events[i]
		assert.Equal(t, i+1, e.Seq)
		assert.Equal(t, "trip", e.Saga)
		assert.Equal(t, "exec-1", e.Execution)
		assert.Equal(t, "writer-1", e.Writer)
		assert.Equal(t, EventStepSucceeded, e.Kind)
		assert.Equal(t, []string{"Hotel"}, e.Changed)
		assert.False(t, e.Time.IsZero())
	}
}

func Test_graceContext(t *testing.T) {
	t.Parallel()

	const grace = time.Minute

	tests := []struct {
		name         string
		releaseFirst bool
		cancelCaller bool
		wait         time.Duration
		wantOpen     bool
		wantCause    error
	}{
		{
			name:     "[Success] When: the caller never cancels ; Should: keep the grace context open with no timer",
			wait:     24 * time.Hour,
			wantOpen: true,
		},
		{
			name:         "[Success] When: the caller cancels ; Should: keep the grace context open until d elapses",
			cancelCaller: true,
			wait:         grace - time.Nanosecond,
			wantOpen:     true,
		},
		{
			name:         "[Fail] When: the caller cancels and d elapses ; Should: end the grace context",
			cancelCaller: true,
			wait:         grace,
			wantOpen:     false,
			wantCause:    ErrGraceEnded,
		},
		{
			name:         "[Fail] When: release runs before the caller cancels ; Should: end the grace context",
			releaseFirst: true,
			cancelCaller: true,
			wantOpen:     false,
			wantCause:    context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.WithValue(t.Context(), graceKey{}, "kept"))
				defer cancel()

				g, release := graceContext(ctx, grace)
				defer release()

				if tt.releaseFirst {
					release()
				}

				if tt.cancelCaller {
					cancel()
				}

				time.Sleep(tt.wait)
				synctest.Wait()

				assert.Equal(t, tt.wantOpen, g.Err() == nil)
				assert.Equal(t, tt.wantCause, context.Cause(g))
				assert.Equal(t, "kept", g.Value(graceKey{}))
			})
		})
	}
}

func Test_graceContext_releaseWaits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "[Success] When: the helper goroutine is still running ; Should: return only after it exits"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				gate := make(chan struct{})
				_, release := graceContext(gatedContext{gate: gate}, time.Minute)

				returned := make(chan struct{})

				go func() {
					release()
					close(returned)
				}()

				synctest.Wait()
				assert.False(t, isClosed(returned), "release returned while the helper goroutine was alive")

				close(gate)
				synctest.Wait()
				assert.True(t, isClosed(returned), "release did not return after the helper goroutine exited")
			})
		})
	}
}

type gatedContext struct {
	gate chan struct{}
}

func (gatedContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c gatedContext) Done() <-chan struct{} {
	<-c.gate

	return nil
}

func (gatedContext) Err() error { return nil }

func (gatedContext) Value(any) any { return nil }

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (r *recorder) step(name string, opts ...StepOption) Option {
	return Step(name, func(context.Context, *tripState) error {
		r.add(name)

		return nil
	}, opts...)
}

func (r *recorder) booking(name, hotel string, opts ...StepOption) Option {
	return Step(name, func(_ context.Context, s *tripState) error {
		r.add(name)

		s.Hotel = hotel

		return nil
	}, opts...)
}

func (r *recorder) when(verdict bool, name string) StepOption {
	return If(func(*tripState) bool {
		r.add("if:" + name)

		return verdict
	})
}

type runRow struct {
	name               string
	steps              func(rec *recorder) []Option
	journal            func() Journal
	runOpts            []RunOption
	runs               int
	concurrent         bool
	wantStatus         Status
	wantErrs           []error
	wantNotErrs        []error
	wantText           []string
	cancelAfter        time.Duration
	cancelCause        error
	wantKinds          map[string]*KindError
	wantNotKinds       map[string]*KindError
	wantAttempts       map[string]int
	wantPreviousIs     map[string][]error
	wantCauseIs        map[string]error
	wantNotCauseIs     map[string]error
	wantCauseText      map[string]string
	wantEventKinds     map[string]string
	wantEventCauseText map[string]string
	wantEventPrevLn    map[string]int
	wantTextCount      map[string]int
	wantExecution      string
	wantSteps          map[string]StepStatus
	wantPending        []string
	wantOrder          [][2]string
	wantZones          []StepZone
	wantTrail          []string
	wantState          tripState
}

func Test_Saga_Run(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(forwardRows(), stopRows(), errorRows()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				rec := &recorder{}
				opts := tt.steps(rec)

				var journal Journal
				if tt.journal != nil {
					journal = tt.journal()
					opts = append(opts, WithJournal(journal))
				}

				s, err := New[tripState]("trip", opts...)
				require.NoError(t, err)

				for _, got := range execute(s, &tt) {
					assertRun(t, &tt, &got, journal)
				}

				assertOrder(t, rec.snapshot(), tt.wantOrder)
			})
		})
	}
}

func assertRun(t *testing.T, tt *runRow, got *ran, journal Journal) {
	t.Helper()

	res, err := &got.res, got.err

	requireErrs(t, err, tt.wantErrs, tt.wantText)

	for _, no := range tt.wantNotErrs {
		require.NotErrorIs(t, err, no)
	}

	assert.Equal(t, tt.wantStatus, res.Status)
	assert.Equal(t, tt.wantPending, res.Pending())
	assert.Equal(t, tt.wantState, got.state)
	assertSteps(t, res, tt.wantSteps)
	assertEvents(t, res, tt, journal)

	assertFailures(t, tt, got)
}

type ran struct {
	res   Result
	err   error
	state tripState
}

func execute(s *Saga[tripState], tt *runRow) []ran {
	out := make([]ran, max(tt.runs, 1))

	var wg sync.WaitGroup

	for i := range out {
		call := func() {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)

			if tt.cancelCause != nil {
				time.AfterFunc(tt.cancelAfter, func() { cancel(tt.cancelCause) })
			}

			out[i].res, out[i].err = s.Run(ctx, &out[i].state, tt.runOpts...)
		}
		if tt.concurrent {
			wg.Go(call)

			continue
		}

		call()
	}

	wg.Wait()

	return out
}

func requireErrs(t *testing.T, err error, wants []error, text []string) {
	t.Helper()

	if len(wants) == 0 && len(text) == 0 {
		require.NoError(t, err)

		return
	}

	require.Error(t, err)

	for _, want := range wants {
		require.ErrorIs(t, err, want)
	}

	for _, part := range text {
		assert.Contains(t, err.Error(), part)
	}
}

func assertSteps(t *testing.T, res *Result, want map[string]StepStatus) {
	t.Helper()

	got := make(map[string]StepStatus, len(res.Steps))
	for i := range res.Steps {
		got[res.Steps[i].Name] = res.Steps[i].Status
	}

	assert.Equal(t, want, got)
}

func assertOrder(t *testing.T, calls []string, want [][2]string) {
	t.Helper()

	for _, pair := range want {
		before, after := slices.Index(calls, pair[0]), slices.Index(calls, pair[1])

		assert.GreaterOrEqual(t, before, 0, "%s never ran in %v", pair[0], calls)
		assert.Greater(t, after, before, "%s did not run after %s in %v", pair[1], pair[0], calls)
	}
}

func assertEvents(t *testing.T, res *Result, tt *runRow, journal Journal) {
	t.Helper()

	events := res.Events()
	require.NotEmpty(t, events)
	assert.Equal(t, EventSagaStarted, events[0].Kind)

	started := map[string]bool{}
	trail := make([]string, 0, len(events))

	for i := range events {
		assert.Equal(t, i+1, events[i].Seq)
		assert.Equal(t, res.Execution, events[i].Execution)

		if events[i].Kind == EventStepStarted {
			started[events[i].Step] = true
		}

		trail = append(trail, trailOf(events[i].Kind, events[i].Step))
	}

	for i := range res.Steps {
		step := &res.Steps[i]
		if slices.Contains([]StepStatus{StepSkipped, StepNotStarted}, step.Status) {
			assert.False(t, started[step.Name], "step %s is %s but has a started event", step.Name, step.Status)
		}

		if slices.Contains([]StepStatus{StepSucceeded, StepUnknown}, step.Status) {
			assert.True(t, started[step.Name], "step %s is %s but has no started event", step.Name, step.Status)
		}
	}

	if tt.wantExecution != "" {
		assert.Equal(t, tt.wantExecution, res.Execution)
	}

	if tt.wantTrail != nil {
		assert.Equal(t, tt.wantTrail, trail)
	}

	if tt.wantZones != nil {
		assert.Equal(t, tt.wantZones, events[0].Steps)
	}

	if stored, ok := journal.(*memoryJournal); ok {
		assert.Equal(t, events, stored.events(res.Execution))
	}
}
