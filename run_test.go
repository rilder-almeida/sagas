package sagas

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
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
	*MemoryJournal

	errs       map[int]error
	failed     map[int]bool
	ctxs       []context.Context
	executions []string
	mu         sync.Mutex
}

func newScriptJournal(errs map[int]error) *scriptJournal {
	return &scriptJournal{MemoryJournal: NewMemoryJournal(), errs: errs, failed: map[int]bool{}}
}

func (j *scriptJournal) Append(ctx context.Context, e *Event) error {
	j.mu.Lock()
	j.ctxs = append(j.ctxs, ctx)

	if e.Seq == 1 && !slices.Contains(j.executions, e.Execution) {
		j.executions = append(j.executions, e.Execution)
	}

	err, scripted := j.errs[e.Seq]
	if scripted && !j.failed[e.Seq] {
		j.failed[e.Seq] = true
		j.mu.Unlock()

		return err
	}

	j.mu.Unlock()

	return j.MemoryJournal.Append(ctx, e)
}

func (j *scriptJournal) results() []result {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]result, 0, len(j.executions))
	for _, execution := range j.executions {
		out = append(out, buildResult(j.Events(execution)))
	}

	return out
}

type orderState struct {
	ID    string
	Hotel string
}

func (s orderState) ExecutionID() string { return s.ID }

type orderStep struct{ Step }

func (*orderStep) Execute(_ context.Context, s *orderState) error {
	s.Hotel = hotelRitz

	return nil
}

const orderID = "order-7"

func Test_Saga_Run_executionID(t *testing.T) {
	t.Parallel()

	completed := []EventKind{EventSagaStarted, EventStepStarted, EventStepSucceeded, EventSagaFinished}

	tests := []struct {
		name       string
		id         string
		script     map[int]error
		wantErrs   [][]error
		wantEvents []EventKind
	}{
		{
			name:     "[Success] When: the state declares ExecutionID ; Should: store the events under that ID",
			id:       orderID,
			wantErrs: [][]error{nil},

			wantEvents: completed,
		},
		{
			name:       "[Fail] When: a second run reuses a stored ID ; Should: return ErrExecutionExists, no RunError",
			id:         orderID,
			wantErrs:   [][]error{nil, {ErrExecutionExists}},
			wantEvents: completed,
		},
		{
			name:     "[Fail] When: the journal fails the saga-started event ; Should: return ErrJournal, no RunError",
			id:       orderID,
			script:   map[int]error{1: errJournalDown},
			wantErrs: [][]error{{ErrJournal, errJournalDown}},
		},
		{
			name:     "[Fail] When: the ID breaks the name rule ; Should: return ErrInvalidRun before any event",
			id:       "order:42",
			wantErrs: [][]error{{ErrInvalidRun}},
		},
		{
			name:     "[Fail] When: the ID is empty ; Should: return ErrInvalidRun before any event",
			id:       "",
			wantErrs: [][]error{{ErrInvalidRun}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			journal := newScriptJournal(tt.script)

			s, err := New[orderState]("order", &orderStep{}, WithJournal(journal))
			require.NoError(t, err)

			for run, wants := range tt.wantErrs {
				runErr := s.Run(t.Context(), &orderState{ID: tt.id})

				requireErrs(t, runErr, wants, nil)
				assert.NotErrorAs(t, runErr, new(*RunError), "run %d: pre-start errors are not a RunError", run)
			}

			var kinds []EventKind
			for _, e := range journal.Events(tt.id) {
				kinds = append(kinds, e.Kind)
			}

			assert.Equal(t, tt.wantEvents, kinds)
		})
	}
}

func Test_Saga_Run_withoutJournal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		runs int
	}{
		{name: "[Success] When: two runs share an ID and the saga has no journal ; Should: complete both", runs: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New[orderState]("order", &orderStep{})
			require.NoError(t, err)

			for range tt.runs {
				st := orderState{ID: orderID}

				require.NoError(t, s.Run(t.Context(), &st))
				assert.Equal(t, hotelRitz, st.Hotel)
			}
		})
	}
}

func Test_checkRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		run           func() (string, error)
		wantExecution *regexp.Regexp
		wantErrs      []error
		wantContains  []string
	}{
		{
			name:          "[Success] When: the state has no ExecutionID ; Should: draw a random 32-char hex execution",
			run:           func() (string, error) { return checkRun(&tripState{}) },
			wantExecution: hexID,
		},
		{
			name:          "[Success] When: the state declares a valid ExecutionID ; Should: keep it",
			run:           func() (string, error) { return checkRun(&orderState{ID: "order-42"}) },
			wantExecution: regexp.MustCompile(`^order-42$`),
		},
		{
			name:         "[Fail] When: the execution ID is empty ; Should: return ErrInvalidRun",
			run:          func() (string, error) { return checkRun(&orderState{}) },
			wantErrs:     []error{ErrInvalidRun},
			wantContains: checkName(""),
		},
		{
			name:         "[Fail] When: the execution ID holds a colon ; Should: return ErrInvalidRun",
			run:          func() (string, error) { return checkRun(&orderState{ID: "order:42"}) },
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
		{
			name:         "[Fail] When: the execution ID starts with a dash ; Should: return ErrInvalidRun",
			run:          func() (string, error) { return checkRun(&orderState{ID: "-x"}) },
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
		{
			name:         "[Fail] When: s is nil ; Should: return ErrInvalidState",
			run:          func() (string, error) { return checkRun[tripState](nil) },
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"state is nil"},
		},
		{
			name:         "[Fail] When: the initial state holds NaN ; Should: return ErrInvalidState",
			run:          func() (string, error) { return checkRun(&floatState{F: math.NaN()}) },
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"F"},
		},
		{
			name:         "[Fail] When: the ID and the state are both bad ; Should: join both errors",
			run:          func() (string, error) { return checkRun(&floatIDState{F: math.NaN(), ID: "a b"}) },
			wantErrs:     []error{ErrInvalidRun, ErrInvalidState},
			wantContains: []string{nameRule, "F"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			execution, err := tt.run()
			requireErrs(t, err, tt.wantErrs, nil)
			requireText(t, err, tt.wantContains, nil)

			if tt.wantExecution == nil {
				assert.Empty(t, execution)

				return
			}

			assert.Regexp(t, tt.wantExecution, execution)
			assert.Empty(t, checkName(execution))
		})
	}
}

type floatIDState struct {
	F  float64
	ID string
}

func (s floatIDState) ExecutionID() string { return s.ID }

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

			j := newScriptJournal(tt.script)
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
			assertAppended(t, j, tt.wantSeq, tt.appends)

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

func assertAppended(t *testing.T, j *scriptJournal, wantStored, appends int) {
	t.Helper()

	events := j.Events("exec-1")
	require.Len(t, events, wantStored)
	require.Len(t, j.ctxs, appends)

	for i := range events {
		e := &events[i]
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

func (r *recorder) do(name string) func(context.Context, *tripState) error {
	return func(context.Context, *tripState) error {
		r.add(name)

		return nil
	}
}

func (r *recorder) book(name, hotel string) func(context.Context, *tripState) error {
	return func(_ context.Context, s *tripState) error {
		r.add(name)

		s.Hotel = hotel

		return nil
	}
}

func (r *recorder) step(name string) *actStep { return act(name, r.do(name)) }

func (r *recorder) booking(name string) *actStep { return act(name, r.book(name, hotelRitz)) }

func (r *recorder) stepIf(name string, pred func(*tripState) bool) *ifStep {
	return gated(name, r.do(name), pred)
}

func (r *recorder) bookingIf(name, hotel string, pred func(*tripState) bool) *ifStep {
	return gated(name, r.book(name, hotel), pred)
}

func (r *recorder) pivot(name string) *pivotStep { return paying(name, r.do(name)) }

func (r *recorder) when(verdict bool, name string) func(*tripState) bool {
	return func(*tripState) bool {
		r.add("if:" + name)

		return verdict
	}
}

type runRow struct {
	name               string
	steps              func(rec *recorder) Flow
	opts               []Option
	script             map[int]error
	runs               int
	concurrent         bool
	wantStatus         Status
	wantErrs           []error
	wantNotErrs        []error
	wantText           []string
	cancelAfter        time.Duration
	cancelCause        error
	wantFailed         []string
	wantKinds          map[string]*KindError
	wantNotKinds       map[string]*KindError
	wantAttempts       map[string]int
	wantPreviousIs     map[string][]error
	wantCauseIs        map[string]error
	wantNotCauseIs     map[string]error
	wantCauseText      map[string]string
	wantEventCauseText map[string]string
	wantTextCount      map[string]int
	wantErrText        map[string]string
	wantUnrecorded     []string
	wantSteps          map[string]StepStatus
	wantPending        []string
	wantOrder          [][2]string
	wantZones          []StepZone
	wantTrail          [][]string
	wantState          tripState
}

func Test_Saga_Run(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(forwardRows(), stopRows(), errorRows()) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				rec := &recorder{}
				journal := newScriptJournal(tt.script)

				s, err := New[tripState]("trip", tt.steps(rec), append(slices.Clone(tt.opts), WithJournal(journal))...)
				require.NoError(t, err)

				runs := execute(s, &tt)
				results := journal.results()
				require.Len(t, results, len(runs))

				for i := range runs {
					assertRun(t, &tt, &runs[i], &results[i], journal)
				}

				assertOrder(t, rec.snapshot(), tt.wantOrder)
			})
		})
	}
}

func assertRun(t *testing.T, tt *runRow, got *ran, res *result, journal *scriptJournal) {
	t.Helper()

	err := got.err

	requireErrs(t, err, tt.wantErrs, tt.wantText)

	for _, no := range tt.wantNotErrs {
		require.NotErrorIs(t, err, no)
	}

	var pending, failed []string

	if err != nil {
		runErr, ok := errors.AsType[*RunError](err)
		require.True(t, ok, "a failed run is not a RunError: %v", err)
		assert.Same(t, ErrIncomplete, runErr.Kind)
		require.ErrorIs(t, err, ErrIncomplete)

		pending = runErr.Pending
		for _, se := range runErr.Steps {
			failed = append(failed, se.Step)
		}
	}

	assert.Equal(t, tt.wantFailed, failed)

	assert.Equal(t, tt.wantStatus, res.Status)
	assert.Equal(t, tt.wantPending, pending)
	assert.Equal(t, tt.wantPending, res.pending())
	assert.Equal(t, tt.wantState, got.state)
	assertSteps(t, res, tt.wantSteps)
	assertEvents(t, res, tt, journal.Events(res.Execution))

	assertFailures(t, tt, got, res)
}

type ran struct {
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

			out[i].err = s.Run(ctx, &out[i].state)
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

func assertSteps(t *testing.T, res *result, want map[string]StepStatus) {
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

func assertEvents(t *testing.T, res *result, tt *runRow, events []Event) {
	t.Helper()

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

	assert.Regexp(t, hexID, res.Execution)
	assertTrail(t, trail, tt.wantTrail)
	assertZones(t, events[0].Steps, tt.wantZones)
}

func seq(events ...string) [][]string {
	groups := make([][]string, len(events))
	for i, e := range events {
		groups[i] = []string{e}
	}

	return groups
}

func together(events ...string) [][]string {
	return [][]string{events}
}

func afterA(rest ...[][]string) [][]string {
	return slices.Concat(append([][][]string{seq(opened, startOf("a"), doneOf("a"))}, rest...)...)
}

func assertTrail(t *testing.T, got []string, want [][]string) {
	t.Helper()

	require.Len(t, got, len(slices.Concat(want...)), "trail %v", got)

	at := 0
	for _, group := range want {
		chunk := got[at : at+len(group)]
		at += len(group)

		assert.ElementsMatch(t, group, chunk, "trail %v", got)

		for _, e := range group {
			step := stepOf(e)
			assert.Equal(t, eventsOf(group, step), eventsOf(chunk, step), "step %q in trail %v", step, got)
		}
	}
}

func stepOf(event string) string {
	_, step, _ := strings.Cut(event, " ")

	return step
}

func eventsOf(trail []string, step string) []string {
	var out []string

	for _, e := range trail {
		if stepOf(e) == step {
			out = append(out, e)
		}
	}

	return out
}

func assertZones(t *testing.T, got, want []StepZone) {
	t.Helper()

	if want == nil {
		for _, z := range got {
			assert.Equal(t, ZoneCompensable, z.Zone, "step %s", z.Name)
		}

		return
	}

	assert.Equal(t, want, got)
}

func Benchmark_Saga_Run(b *testing.B) {
	s, err := New[tripState]("trip", Sequence(act("a", okAction), act("b", okAction)))
	require.NoError(b, err)

	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		if runErr := s.Run(ctx, &tripState{}); runErr != nil {
			b.Fatal(runErr)
		}
	}
}
