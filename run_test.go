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
	"sync/atomic"
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

var (
	errAwaitDone      = errors.New("sagas: test: wait for the context to end")
	errAwaitThenStore = errors.New("sagas: test: wait for the context to end, then store this and every later event")
)

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

const forever = -1

type repeatedError struct {
	error

	times int
}

func (r *repeatedError) Unwrap() error { return r.error }

func failing(err error, times int) error { return &repeatedError{error: err, times: times} }

type scriptJournal struct {
	*MemoryJournal

	errs       map[int]error
	failed     map[int]int
	breaksCtx  bool
	ctxs       []context.Context
	executions []string
	mu         sync.Mutex
}

func newScriptJournal(errs map[int]error) *scriptJournal {
	return &scriptJournal{MemoryJournal: NewMemoryJournal(), errs: errs, failed: map[int]int{}}
}

func (j *scriptJournal) Append(ctx context.Context, e *Event) error {
	j.mu.Lock()
	j.ctxs = append(j.ctxs, ctx)

	if e.Seq == 1 && !slices.Contains(j.executions, e.Execution) {
		j.executions = append(j.executions, e.Execution)
	}

	err, scripted := j.errs[e.Seq]

	limit := 1
	if r, ok := errors.AsType[*repeatedError](err); ok {
		err, limit = r.error, r.times
	}

	if scripted && (limit == forever || j.failed[e.Seq] < limit) {
		j.failed[e.Seq]++
		j.mu.Unlock()

		if errors.Is(err, errAwaitDone) {
			<-ctx.Done()

			return ctx.Err()
		}

		if errors.Is(err, errAwaitThenStore) {
			<-ctx.Done()

			j.mu.Lock()
			j.breaksCtx = true
			j.mu.Unlock()

			return j.MemoryJournal.Append(context.WithoutCancel(ctx), e)
		}

		return err
	}

	if j.breaksCtx {
		ctx = context.WithoutCancel(ctx)
	}

	j.mu.Unlock()

	return j.MemoryJournal.Append(ctx, e)
}

type runRecord struct {
	Execution string
	Status    Status
	Steps     []stepResult
	events    []Event
}

func recordOf(events []Event) runRecord {
	r := runRecord{Status: StatusUnfinished, Steps: foldStepStatuses(events), events: events}

	for i := range events {
		if events[i].Kind == EventSagaStarted {
			r.Execution = events[i].Execution
		}

		if events[i].Kind == EventSagaFinished {
			r.Status = events[i].Status
		}
	}

	return r
}

func (j *scriptJournal) results() []runRecord {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]runRecord, 0, len(j.executions))
	for _, execution := range j.executions {
		out = append(out, recordOf(j.Events(execution)))
	}

	return out
}

type orderState struct {
	ID    string
	Hotel string
}

func (s orderState) ExecutionID() string { return s.ID }

type orderStep struct {
	Step

	executes *atomic.Int64
}

func (o *orderStep) Execute(_ context.Context, s *orderState) error {
	o.executes.Add(1)
	time.Sleep(pause)

	s.Hotel = hotelRitz

	return nil
}

const orderID = "order-7"

type executionRow struct {
	name         string
	id           string
	script       map[int]error
	concurrent   bool
	cancelAfter  time.Duration
	wantErrs     [][]error
	wantNotErrs  []error
	wantExecutes int64
	wantEvents   []EventKind
}

func Test_Saga_Run_executionID(t *testing.T) {
	t.Parallel()

	completed := []EventKind{EventSagaStarted, EventStepStarted, EventStepSucceeded, EventSagaFinished}

	tests := []executionRow{
		{
			name:         "[Success] When: the state declares ExecutionID ; Should: store the events under that ID",
			id:           orderID,
			wantErrs:     [][]error{nil},
			wantNotErrs:  []error{ErrJournal},
			wantExecutes: 1,
			wantEvents:   completed,
		},
		{
			name: "[Fail] When: a second run reuses a stored ID ; " +
				"Should: return ErrExecutionExists, no RunError",
			id:           orderID,
			wantErrs:     [][]error{nil, {ErrExecutionExists}},
			wantNotErrs:  []error{ErrJournal},
			wantExecutes: 1,
			wantEvents:   completed,
		},
		{
			name: "[Fail] When: two runs share one ID on one journal at once ; " +
				"Should: complete one and return ErrExecutionExists, no RunError, to the other",
			id:           orderID,
			concurrent:   true,
			wantErrs:     [][]error{nil, {ErrExecutionExists}},
			wantNotErrs:  []error{ErrJournal},
			wantExecutes: 1,
			wantEvents:   completed,
		},
		{
			name:     "[Fail] When: the journal fails the saga-started event ; Should: return ErrJournal, no RunError",
			id:       orderID,
			script:   map[int]error{1: errJournalDown},
			wantErrs: [][]error{{ErrJournal, errJournalDown}},
		},
		{
			name: "[Fail] When: the saga-started append fails at the grace end ; " +
				"Should: return the journal error as is, without ErrGraceEnded and no RunError",
			id:          orderID,
			script:      map[int]error{1: errAwaitDone},
			cancelAfter: pause,
			wantErrs:    [][]error{{ErrJournal, context.Canceled}},
			wantNotErrs: []error{ErrGraceEnded, errStop},
		},
		{
			name:        "[Fail] When: the ID breaks the name rule ; Should: return ErrInvalidRun before any event",
			id:          "order:42",
			wantErrs:    [][]error{{ErrInvalidRun}},
			wantNotErrs: []error{ErrJournal},
		},
		{
			name:        "[Fail] When: the ID is empty ; Should: return ErrInvalidRun before any event",
			id:          "",
			wantErrs:    [][]error{{ErrInvalidRun}},
			wantNotErrs: []error{ErrJournal},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				journal := newScriptJournal(tt.script)
				step := &orderStep{executes: &atomic.Int64{}}

				s, err := New[orderState]("order", step, WithJournal(journal))
				require.NoError(t, err)

				errs := runOrders(t.Context(), s, &tt)

				for run, wants := range tt.wantErrs {
					requireErrs(t, errs[run], wants, nil)
					assert.NotErrorAs(t, errs[run], new(*RunError), "run %d: pre-start errors are not a RunError", run)

					for _, no := range tt.wantNotErrs {
						require.NotErrorIs(t, errs[run], no)
					}
				}

				assert.Equal(t, tt.wantExecutes, step.executes.Load())

				var kinds []EventKind
				for _, e := range journal.Events(tt.id) {
					kinds = append(kinds, e.Kind)
				}

				assert.Equal(t, tt.wantEvents, kinds)
			})
		})
	}
}

func runOrders(ctx context.Context, s *Saga[orderState], tt *executionRow) []error {
	errs := make([]error, len(tt.wantErrs))

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	if tt.cancelAfter > 0 {
		time.AfterFunc(tt.cancelAfter, func() { cancel(errStop) })
	}

	var wg sync.WaitGroup

	for i := range errs {
		call := func() { errs[i] = s.Run(ctx, &orderState{ID: tt.id}) }
		if tt.concurrent {
			wg.Go(call)

			continue
		}

		call()
	}

	wg.Wait()
	slices.SortStableFunc(errs, func(a, b error) int { return cmpNil(a) - cmpNil(b) })

	return errs
}

func cmpNil(err error) int {
	if err == nil {
		return 0
	}

	return 1
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

			synctest.Test(t, func(t *testing.T) {
				s, err := New[orderState]("order", &orderStep{executes: &atomic.Int64{}})
				require.NoError(t, err)

				for range tt.runs {
					st := orderState{ID: orderID}

					require.NoError(t, s.Run(t.Context(), &st))
					assert.Equal(t, hotelRitz, st.Hotel)
				}
			})
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
		name       string
		script     map[int]error
		appends    int
		failAt     int
		wantStored int
		wantErrs   []error
		wantNoErrs []error
		wantCauses []error
	}{
		{
			name:       "[Success] When: the first event is appended ; Should: give it Seq 1 and the run's Writer",
			appends:    1,
			wantStored: 1,
		},
		{
			name:       "[Success] When: two events are appended ; Should: number them 1 and 2",
			appends:    2,
			wantStored: 2,
		},
		{
			name:       "[Fail] When: the journal returns a plain error ; Should: wrap ErrJournal and keep seq",
			script:     map[int]error{1: errAlwaysFails},
			appends:    2,
			failAt:     1,
			wantStored: 1,
			wantErrs:   []error{ErrJournal, errAlwaysFails},
			wantNoErrs: []error{ErrConcurrentWriter, ErrExecutionExists},
			wantCauses: []error{ErrJournal},
		},
		{
			name:       "[Fail] When: the journal rejects Seq 1 ; Should: return ErrExecutionExists as is and keep seq",
			script:     map[int]error{1: fmt.Errorf("%w: exec-1", ErrExecutionExists)},
			appends:    2,
			failAt:     1,
			wantStored: 1,
			wantErrs:   []error{ErrExecutionExists},
			wantNoErrs: []error{ErrJournal},
			wantCauses: []error{ErrExecutionExists},
		},
		{
			name:       "[Fail] When: the journal rejects Seq 2 ; Should: return ErrConcurrentWriter and keep seq",
			script:     map[int]error{2: fmt.Errorf("%w: exec-1", ErrConcurrentWriter)},
			appends:    3,
			failAt:     2,
			wantStored: 2,
			wantErrs:   []error{ErrConcurrentWriter},
			wantNoErrs: []error{ErrJournal},
			wantCauses: []error{ErrConcurrentWriter},
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

			require.Equal(t, tt.failAt == 0, len(tt.wantErrs) == 0, "failAt and wantErrs must agree")
			require.True(t, tt.failAt >= 0 && tt.failAt <= tt.appends, "failAt must be within 0..appends")

			for i := 1; i <= tt.appends; i++ {
				changed := []string{"Hotel"}
				err = r.append(ctx, &Event{Kind: EventStepSucceeded, Step: "a", Changed: changed})
				changed[0] = "mutated"

				wantErrs := tt.wantErrs
				if i != tt.failAt {
					wantErrs = nil
				}

				requireAppendErr(t, err, wantErrs, tt.wantNoErrs)
			}

			assertAppended(t, j, tt.wantStored, tt.appends)

			for _, got := range j.ctxs {
				assert.Equal(t, ctx, got)
			}

			causes := r.causes()
			require.Len(t, causes, len(tt.wantCauses))

			for i, want := range tt.wantCauses {
				require.ErrorIs(t, causes[i], want)
			}
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

func (r *recorder) pivot() *pivotStep { return paying("p", r.do("p")) }

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
	wantKind           *KindError
	wantErrs           []error
	wantNotErrs        []error
	wantText           []string
	deadline           time.Duration
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
	wantSteps          map[string]stepStatus
	wantPending        []string
	wantJournalOnly    []string
	wantRunOnly        []string
	wantOrder          [][2]string
	wantZones          []StepZone
	wantTrail          [][]string
	wantState          tripState
}

func Test_Saga_Run(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(forwardRows(), pivotRows(), stopRows(), errorRows(), backwardRows(), runRows()) {
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

func assertRun(t *testing.T, tt *runRow, got *ran, res *runRecord, journal *scriptJournal) {
	t.Helper()

	err := got.err

	requireErrs(t, err, tt.wantErrs, tt.wantText)

	for _, no := range tt.wantNotErrs {
		require.NotErrorIs(t, err, no)
	}

	var pending, failed []string

	if tt.wantKind == nil {
		require.NoError(t, err)
	} else {
		runErr, ok := errors.AsType[*RunError](err)
		require.True(t, ok, "a failed run is not a RunError: %v", err)
		assert.Same(t, tt.wantKind, runErr.Kind)
		require.ErrorIs(t, err, tt.wantKind)
		assertExclusive(t, err)

		pending = runErr.Pending
		for _, se := range runErr.Steps {
			failed = append(failed, keyOf(se))
		}
	}

	assert.Equal(t, tt.wantFailed, failed)

	assert.Equal(t, tt.wantStatus, res.Status)
	assert.Equal(t, tt.wantPending, pending)
	assert.Equal(t, journalPending(res, tt), pendingOf(res.events))
	assert.Equal(t, tt.wantState, got.state)
	assertSteps(t, res, tt.wantSteps)
	assertEvents(t, res, tt, journal.Events(res.Execution))

	assertFailures(t, tt, got, res)
}

func journalPending(res *runRecord, tt *runRow) []string {
	var want []string

	for i := range res.Steps {
		name := res.Steps[i].Name
		if slices.Contains(tt.wantRunOnly, name) {
			continue
		}

		if slices.Contains(tt.wantPending, name) || slices.Contains(tt.wantJournalOnly, name) {
			want = append(want, name)
		}
	}

	return want
}

func assertExclusive(t *testing.T, err error) {
	t.Helper()

	assert.False(t, errors.Is(err, ErrCompensated) && errors.Is(err, ErrCompensationFailed),
		"the error matches both ErrCompensated and ErrCompensationFailed: %v", err)
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

			if tt.deadline > 0 {
				var expire context.CancelFunc

				ctx, expire = context.WithTimeout(ctx, tt.deadline)
				defer expire()
			}

			switch {
			case tt.cancelCause == nil:
			case tt.cancelAfter == 0:
				cancel(tt.cancelCause)
			default:
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

func assertSteps(t *testing.T, res *runRecord, want map[string]stepStatus) {
	t.Helper()

	got := make(map[string]stepStatus, len(res.Steps))
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

func assertEvents(t *testing.T, res *runRecord, tt *runRow, events []Event) {
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
		if slices.Contains([]stepStatus{stepSkipped, stepNotStarted}, step.Status) {
			assert.False(t, started[step.Name], "step %s is %s but has a started event", step.Name, step.Status)
		}

		if slices.Contains([]stepStatus{stepSucceeded, stepUnknown}, step.Status) {
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

func runRows() []runRow {
	return []runRow{
		finishedAfterCompensatedRow(),
		finishedAfterCompensationFailedRow(),
		finishedAfterIncompleteRow(),
		pivotOutcomeAppendRow(),
		forwardOnlyIntentRow(),
		undoIntentRejectedRow(),
		undoRunningRejectedRow(),
		undoOutcomeRejectedRow(),
		turnAfterRejectionRow(),
		forwardIntentAtGraceEndRow(),
		pivotOutcomeAtGraceEndRow(),
		finishedAtGraceEndRow(),
		finishedBeforeGraceEndRow(),
		skippedAtGraceEndRow(),
	}
}

func skippedAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: the step-skipped append of a forward-only step fails at the grace end ; " +
			"Should: end ErrIncomplete and match ErrJournal, ErrGraceEnded and the caller cause",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), rec.stepIf("z", rec.when(false, "z")))
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		script:      map[int]error{6: errAwaitDone},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  StatusUnfinished,
		wantKind:    ErrIncomplete,
		wantErrs:    []error{ErrGraceEnded, errStop},
		wantNotErrs: []error{ErrCompensated, ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepNotStarted},
		wantPending: []string{"z"},
		wantOrder:   [][2]string{{"p", "if:z"}},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p")),
		wantZones:   pivotZones("z"),
	}
}

func forwardIntentAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: the intent append of a forward-only step fails at the grace end ; " +
			"Should: end ErrIncomplete and match ErrJournal, ErrGraceEnded and the caller cause",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), rec.step("z"))
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		script:      map[int]error{6: errAwaitDone},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  StatusUnfinished,
		wantKind:    ErrIncomplete,
		wantErrs:    []error{ErrGraceEnded, errStop, context.Canceled},
		wantNotErrs: []error{ErrCompensated, ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepNotStarted},
		wantPending: []string{"z"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p")),
		wantZones:   pivotZones("z"),
	}
}

func pivotOutcomeAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of the pivot fails at the grace end ; " +
			"Should: leave the pivot unknown and match ErrJournal, ErrGraceEnded and the caller cause",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), rec.step("z"))
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		script:      map[int]error{5: errAwaitDone},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  StatusUnfinished,
		wantKind:    ErrIncomplete,
		wantErrs:    []error{ErrGraceEnded, errStop},
		wantNotErrs: []error{ErrCompensated, ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepUnknown, "z": stepNotStarted},
		wantPending: []string{"p", "z"},
		wantRunOnly: []string{"z"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p")),
		wantZones:   pivotZones("z"),
	}
}

func finishedAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append of a compensated run fails at the grace end ; " +
			"Should: keep ErrCompensated and match ErrJournal, ErrGraceEnded and the caller cause",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), once("b", alwaysFails))
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		script:       map[int]error{8: errAwaitDone},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusUnfinished,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"b": ErrFailed},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"b": 1},
		wantNotErrs:  []error{ErrJournal},
		wantErrs:     []error{errAlwaysFails, ErrGraceEnded, errStop},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "b": stepFailed},
		wantOrder:    [][2]string{{"a", undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), failedOf("b"), compStartOf("a"), undoneOf("a"),
		),
	}
}

func finishedBeforeGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append fails after a caller cancel and before any grace end ; " +
			"Should: keep ErrCompensated and match neither ErrGraceEnded nor the caller cause",
		steps: func(rec *recorder) Flow {
			slowUndo := func(context.Context, *tripState) error {
				time.Sleep(pause)
				rec.add(undoA)

				return nil
			}

			return Sequence(undoing("a", rec.do("a"), slowUndo), once("b", alwaysFails))
		},
		script:       map[int]error{8: errJournalDown},
		cancelAfter:  pause / 2,
		cancelCause:  errStop,
		wantStatus:   StatusUnfinished,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"b": ErrFailed},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"b": 1},
		wantErrs:     []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantNotErrs:  []error{ErrGraceEnded, errStop},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "b": stepFailed},
		wantOrder:    [][2]string{{"a", undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), failedOf("b"), compStartOf("a"), undoneOf("a"),
		),
	}
}

func finishedAfterCompensatedRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append of a compensated run fails ; " +
			"Should: keep ErrCompensated and match ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), once("b", alwaysFails))
		},
		script:       map[int]error{8: errJournalDown},
		wantStatus:   StatusUnfinished,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"b": ErrFailed},
		wantFailed:   []string{"b"},
		wantErrText:  map[string]string{"b": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"b": 1},
		wantErrs:     []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "b": stepFailed},
		wantOrder:    [][2]string{{"a", undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), failedOf("b"), compStartOf("a"), undoneOf("a"),
		),
	}
}

func finishedAfterCompensationFailedRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append of a compensation-failed run fails ; " +
			"Should: keep ErrCompensationFailed and match ErrJournal",
		steps: func(*recorder) Flow {
			return Sequence(undoOnce("a", okAction, alwaysFails), once("b", alwaysFails))
		},
		script:       map[int]error{8: errJournalDown},
		wantStatus:   StatusUnfinished,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{"b": ErrFailed, comp("a"): ErrFailed},
		wantFailed:   []string{"b", comp("a")},
		wantErrText:  map[string]string{"b": errAlwaysFails.Error(), comp("a"): errAlwaysFails.Error()},
		wantAttempts: map[string]int{"b": 1, comp("a"): 1},
		wantErrs:     []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantSteps:    map[string]stepStatus{"a": stepCompensationFailed, "b": stepFailed},
		wantPending:  []string{"a"},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), failedOf("b"), compStartOf("a"), undoFailedOf("a"),
		),
	}
}

func pivotZones(forward ...string) []StepZone {
	zones := make([]StepZone, 0, len(forward)+2)
	zones = append(zones, StepZone{Name: "a", Zone: ZoneCompensable}, StepZone{Name: "p", Zone: ZonePivot})

	for _, name := range forward {
		zones = append(zones, StepZone{Name: name, Zone: ZoneForwardOnly})
	}

	return zones
}

func finishedAfterIncompleteRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append of an incomplete run fails ; " +
			"Should: keep ErrIncomplete and match ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), once("z", alwaysFails))
		},
		script:       map[int]error{8: errJournalDown},
		wantStatus:   StatusUnfinished,
		wantKind:     ErrIncomplete,
		wantKinds:    map[string]*KindError{"z": ErrFailed},
		wantFailed:   []string{"z"},
		wantErrText:  map[string]string{"z": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"z": 1},
		wantErrs:     []error{errAlwaysFails, ErrJournal, errJournalDown},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepFailed},
		wantPending:  []string{"z"},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z"), failedOf("z"),
		),
		wantZones: pivotZones("z"),
	}
}

func pivotOutcomeAppendRow() runRow {
	return runRow{
		name: "[Fail] When: the outcome append of a successful pivot fails ; " +
			"Should: leave the pivot unknown, compensate nothing and end ErrIncomplete",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), rec.step("z"))
		},
		script:      map[int]error{5: errJournalDown},
		wantStatus:  StatusIncomplete,
		wantKind:    ErrIncomplete,
		wantErrs:    []error{ErrJournal, errJournalDown},
		wantNotErrs: []error{ErrCompensated},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepUnknown, "z": stepNotStarted},
		wantPending: []string{"p", "z"},
		wantRunOnly: []string{"z"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p"), closed),
		wantZones:   pivotZones("z"),
	}
}

func forwardOnlyIntentRow() runRow {
	return runRow{
		name: "[Fail] When: the intent append of a forward-only step fails ; " +
			"Should: not start it, compensate nothing and end ErrIncomplete",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), rec.step("z"))
		},
		script:      map[int]error{6: errJournalDown},
		wantStatus:  StatusIncomplete,
		wantKind:    ErrIncomplete,
		wantErrs:    []error{ErrJournal, errJournalDown},
		wantNotErrs: []error{ErrCompensated},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepNotStarted},
		wantPending: []string{"z"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), closed),
		wantZones:   pivotZones("z"),
	}
}

func undoIntentRejectedRow() runRow {
	return runRow{
		name: "[Fail] When: the journal rejects a compensation intent ; " +
			"Should: end ErrConcurrentWriter and fail no compensation",
		steps: func(rec *recorder) Flow {
			b := failingUndo("b", CompensationContinue, rec.do(undoB))

			return Sequence(rec.undoable("a"), b, once("c", alwaysFails))
		},
		script:       map[int]error{8: fmt.Errorf("%w: other writer", ErrConcurrentWriter)},
		wantStatus:   StatusUnfinished,
		wantKind:     ErrConcurrentWriter,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{ErrConcurrentWriter, errAlwaysFails},
		wantNotErrs:  []error{ErrJournal},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded, "c": stepFailed},
		wantTrail:    chainTrail(),
	}
}

func rejectedUndoFlow(undo func(context.Context, *tripState) error) func(*recorder) Flow {
	return func(rec *recorder) Flow {
		return Sequence(Parallel(
			Sequence(rec.undoable("x"), rec.undoable("b")),
			Sequence(rec.undoable("y"), failingUndo("d", CompensationContinue, undo)),
		), once("c", alwaysFails))
	}
}

func rejectedUndoTrail() [][]string {
	return slices.Concat(
		seq(opened),
		together(
			startOf("x"), startOf("y"), doneOf("x"), doneOf("y"), startOf("b"), startOf("d"), doneOf("b"), doneOf("d"),
		),
		seq(startOf("c"), failedOf("c"), compStartOf("d")),
	)
}

func undoOutcomeRejectedRow() runRow {
	return runRow{
		name: "[Fail] When: the journal rejects the outcome append of a successful compensation ; " +
			"Should: end ErrConcurrentWriter without failing that compensation",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"), once("c", alwaysFails))
		},
		script:       map[int]error{9: ErrConcurrentWriter},
		wantStatus:   StatusUnfinished,
		wantKind:     ErrConcurrentWriter,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{ErrConcurrentWriter, errAlwaysFails},
		wantNotErrs:  []error{ErrJournal},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:  []string{"b"},
		wantOrder:    [][2]string{{"b", undoB}},
		wantTrail:    chainTrail(compStartOf("b")),
	}
}

func undoRunningRejectedRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation succeeds after the journal rejected an event ; " +
			"Should: commit and append nothing for it",
		steps: rejectedUndoFlow(func(_ context.Context, s *tripState) error {
			time.Sleep(pause)

			s.Hotel = undoneHotel

			return nil
		}),
		script:       map[int]error{13: ErrConcurrentWriter},
		wantStatus:   StatusUnfinished,
		wantKind:     ErrConcurrentWriter,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{ErrConcurrentWriter, errAlwaysFails},
		wantNotErrs:  []error{ErrJournal},
		wantSteps: map[string]stepStatus{
			"x": stepSucceeded, "y": stepSucceeded, "b": stepSucceeded, "d": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"d"},
		wantTrail:   rejectedUndoTrail(),
	}
}

func turnAfterRejectionRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation's turn comes after a rejection and the grace end ; " +
			"Should: neither run nor fail it",
		steps:        rejectedUndoFlow(lateFailure),
		opts:         []Option{WithRecoveryTimeout(pause)},
		script:       map[int]error{13: ErrConcurrentWriter},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   StatusUnfinished,
		wantKind:     ErrConcurrentWriter,
		wantKinds:    map[string]*KindError{"c": ErrFailed, comp("d"): ErrFailed},
		wantFailed:   []string{"c", comp("d")},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error(), comp("d"): errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1, comp("d"): 1},
		wantCauseIs:  map[string]error{comp("d"): ErrGraceEnded},
		wantErrs:     []error{ErrConcurrentWriter, errAlwaysFails, ErrGraceEnded, errStop},
		wantNotErrs:  []error{ErrJournal},
		wantSteps: map[string]stepStatus{
			"x": stepSucceeded, "y": stepSucceeded, "b": stepSucceeded, "d": stepCompensationFailed, "c": stepFailed,
		},
		wantPending:    []string{"d"},
		wantUnrecorded: []string{comp("d")},
		wantTrail:      rejectedUndoTrail(),
	}
}

func Test_endKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        Status
		rejected      bool
		journalFailed bool
		want          *KindError
	}{
		{
			name:   "[Success] When: every started compensation succeeded ; Should: return ErrCompensated",
			status: StatusCompensated,
			want:   ErrCompensated,
		},
		{
			name:          "[Success] When: a compensated run also had a journal failure ; Should: keep ErrCompensated",
			status:        StatusCompensated,
			journalFailed: true,
			want:          ErrCompensated,
		},
		{
			name:   "[Success] When: a compensation failed for good ; Should: return ErrCompensationFailed",
			status: StatusCompensationFailed,
			want:   ErrCompensationFailed,
		},
		{
			name:   "[Success] When: forward work is undone ; Should: return ErrIncomplete",
			status: StatusIncomplete,
			want:   ErrIncomplete,
		},
		{
			name:          "[Success] When: every step succeeded and the journal failed ; Should: return ErrJournal",
			status:        StatusCompleted,
			journalFailed: true,
			want:          ErrJournal,
		},
		{
			name:     "[Success] When: the journal rejected an event ; Should: return ErrConcurrentWriter",
			rejected: true,
			want:     ErrConcurrentWriter,
		},
		{
			name:     "[Success] When: a rejection follows the status ; Should: return ErrConcurrentWriter",
			status:   StatusCompleted,
			rejected: true,
			want:     ErrConcurrentWriter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Same(t, tt.want, endKind(tt.status, tt.rejected, tt.journalFailed))
		})
	}
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

func Test_callerCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cause   error
		cancel  bool
		wantIs  []error
		wantNil bool
	}{
		{
			name:    "[Success] When: the context is not done ; Should: return nil",
			wantNil: true,
		},
		{
			name:   "[Success] When: the context is canceled without a cause ; Should: match context.Canceled",
			cancel: true,
			wantIs: []error{context.Canceled},
		},
		{
			name: "[Success] When: the context is canceled with a cause ; " +
				"Should: match the cause and context.Canceled",
			cause:  errStop,
			cancel: true,
			wantIs: []error{errStop, context.Canceled},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)

			if tt.cancel {
				cancel(tt.cause)
			}

			got := callerCause(ctx)
			assert.Equal(t, tt.wantNil, got == nil)

			for _, want := range tt.wantIs {
				require.ErrorIs(t, got, want)
			}
		})
	}
}
