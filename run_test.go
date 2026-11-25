package sagas

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rilder-almeida/sagas/internal/serial"
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
	errAwaitCause     = errors.New("sagas: test: wait for the context to end, then return its cause")
	errAwaitThenKeep  = errors.New("sagas: test: wait for the context to end, then store this event only")
	errStoreThenFail  = errors.New("sagas: test: store this event, let time pass, then return an error")
)

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

const forever = -1

type repeatedError struct {
	error

	times int
}

func (r *repeatedError) Unwrap() error { return r.error }

func failing(err error, times int) error { return &repeatedError{error: err, times: times} }

type seriesError struct {
	errs []error
}

func (s *seriesError) Error() string { return errors.Join(s.errs...).Error() }

func inTurn(errs ...error) error { return &seriesError{errs: errs} }

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

	if s, ok := errors.AsType[*seriesError](err); ok {
		limit = len(s.errs)
		err = s.errs[min(j.failed[e.Seq], limit-1)]
	}

	if scripted && (limit == forever || j.failed[e.Seq] < limit) {
		j.failed[e.Seq]++
		j.mu.Unlock()

		return j.scripted(ctx, e, err)
	}

	if j.breaksCtx {
		ctx = context.WithoutCancel(ctx)
	}

	j.mu.Unlock()

	return j.MemoryJournal.Append(ctx, e)
}

func (j *scriptJournal) scripted(ctx context.Context, e *Event, err error) error {
	switch {
	case errors.Is(err, errAwaitDone):
		<-ctx.Done()

		return ctx.Err()
	case errors.Is(err, errAwaitCause):
		<-ctx.Done()

		return context.Cause(ctx)
	case errors.Is(err, errAwaitThenKeep):
		<-ctx.Done()

		return j.MemoryJournal.Append(context.WithoutCancel(ctx), e)
	case errors.Is(err, errStoreThenFail):
		if stored := j.MemoryJournal.Append(ctx, e); stored != nil {
			return stored
		}

		time.Sleep(time.Millisecond)

		return err
	case errors.Is(err, errAwaitThenStore):
		<-ctx.Done()

		j.mu.Lock()
		j.breaksCtx = true
		j.mu.Unlock()

		return j.MemoryJournal.Append(context.WithoutCancel(ctx), e)
	default:
		return err
	}
}

const noEndStatus Status = "unfinished"

type runRecord struct {
	Execution string
	Status    Status
	End       EventKind
	Steps     []stepResult
	events    []Event
}

func recordOf(events []Event) runRecord {
	r := runRecord{Status: noEndStatus, Steps: foldStepStatuses(events), events: events}

	for i := range events {
		if events[i].Kind == EventSagaStarted {
			r.Execution = events[i].Execution
		}

		if events[i].Kind == EventSagaFinished || events[i].Kind == EventSagaSuspended {
			r.Status, r.End = events[i].Status, events[i].Kind
		}
	}

	return r
}

func (j *scriptJournal) results(tb testing.TB) []runRecord {
	tb.Helper()

	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]runRecord, 0, len(j.executions))
	for _, execution := range j.executions {
		out = append(out, recordOf(storedEvents(tb, j.MemoryJournal, execution)))
	}

	return out
}

type orderState struct {
	Hotel Hotel
	ID    string `sagas:"id"`
}

type orderStep struct {
	Step[orderState, Hotel]

	executes *atomic.Int64
}

func (o *orderStep) Execute(context.Context, orderState) (Hotel, error) {
	o.executes.Add(1)
	time.Sleep(pause)

	return hotelRitz, nil
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
			name:         "[Success] When: the state tags its ID field ; Should: store the events under that ID",
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
				for _, e := range storedEvents(t, journal.MemoryJournal, tt.id) {
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
					assert.Equal(t, Hotel(hotelRitz), st.Hotel)
				}
			})
		})
	}
}

func Test_checkRun(t *testing.T) {
	t.Parallel()

	loop := &linkState{}
	loop.Next = loop

	tests := []struct {
		name          string
		run           func() (runStart, error)
		wantExecution *regexp.Regexp
		wantFields    int
		wantErrs      []error
		wantContains  []string
		wantAbsent    []string
	}{
		{
			name:          "[Success] When: the state has no id tag ; Should: draw a random 32-char hex execution",
			run:           func() (runStart, error) { return checkRun(&tripState{}, -1) },
			wantExecution: hexID,
			wantFields:    3,
		},
		{
			name:          "[Success] When: the tagged field holds a valid ID ; Should: keep it",
			run:           func() (runStart, error) { return checkRun(&orderState{ID: "order-42"}, 1) },
			wantExecution: regexp.MustCompile(`^order-42$`),
			wantFields:    2,
		},
		{
			name:         "[Fail] When: the execution ID is empty ; Should: return ErrInvalidRun",
			run:          func() (runStart, error) { return checkRun(&orderState{}, 1) },
			wantErrs:     []error{ErrInvalidRun},
			wantContains: checkName(""),
		},
		{
			name:         "[Fail] When: the execution ID holds a colon ; Should: return ErrInvalidRun",
			run:          func() (runStart, error) { return checkRun(&orderState{ID: "order:42"}, 1) },
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
		{
			name:         "[Fail] When: the execution ID starts with a dash ; Should: return ErrInvalidRun",
			run:          func() (runStart, error) { return checkRun(&orderState{ID: "-x"}, 1) },
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
		{
			name: "[Fail] When: the execution ID holds quotes, a backslash and non-ASCII text ; " +
				"Should: return ErrInvalidRun",
			run:          func() (runStart, error) { return checkRun(&orderState{ID: quotedID}, 1) },
			wantErrs:     []error{ErrInvalidRun},
			wantContains: []string{nameRule},
		},
		{
			name:         "[Fail] When: s is nil ; Should: return ErrInvalidState",
			run:          func() (runStart, error) { return checkRun[tripState](nil, -1) },
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"state is nil"},
		},
		{
			name:         "[Fail] When: the initial state holds NaN ; Should: return ErrInvalidState",
			run:          func() (runStart, error) { return checkRun(&floatState{F: math.NaN()}, -1) },
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"F"},
		},
		{
			name: "[Fail] When: the initial state holds a pointer cycle ; " +
				"Should: return ErrInvalidState naming the field",
			run: func() (runStart, error) {
				st := baseState()
				st.Link = loop

				return checkRun(&st, -1)
			},
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"sagas: invalid state: Link: "},
		},
		{
			name: "[Fail] When: the ID and the state are both bad ; Should: join both errors",
			run: func() (runStart, error) {
				return checkRun(&floatIDState{F: math.NaN(), ID: "a b"}, 1)
			},
			wantErrs:     []error{ErrInvalidRun, ErrInvalidState},
			wantContains: []string{nameRule, "F"},
		},
		{
			name: "[Fail] When: the ID field holds invalid UTF-8 ; " +
				"Should: return only the encoding error of the field, no ID rule",
			run:          func() (runStart, error) { return checkRun(&orderState{ID: "ab\xff"}, 1) },
			wantErrs:     []error{ErrInvalidState},
			wantContains: []string{"sagas: invalid state: ID: "},
			wantAbsent:   []string{"empty name", "execution ID", ErrInvalidRun.Error()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			start, err := tt.run()
			requireErrs(t, err, tt.wantErrs, nil)
			requireText(t, err, tt.wantContains, tt.wantAbsent)
			assert.Len(t, start.fields, tt.wantFields)

			execution := start.execution

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
	ID string `sagas:"id"`
}

const quotedID = `say "hi" \ café`

func encodedOf(s any) func() ([][]byte, error) {
	return func() ([][]byte, error) { return encodeFields(reflect.ValueOf(s)) }
}

func Test_executionOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fields  func() ([][]byte, error)
		idField int
		want    *regexp.Regexp
		wantErr error
	}{
		{
			name:    "[Success] When: the id field is tagged ; Should: return its value",
			fields:  encodedOf(orderState{ID: "order-9", Hotel: "x"}),
			idField: 1,
			want:    regexp.MustCompile(`^order-9$`),
		},
		{
			name:    "[Success] When: the id field is not the first ; Should: return the value at idField",
			fields:  encodedOf(floatIDState{F: 1, ID: "late-id"}),
			idField: 1,
			want:    regexp.MustCompile(`^late-id$`),
		},
		{
			name: "[Success] When: the ID holds quotes, a backslash and non-ASCII text ; " +
				"Should: return it unchanged",
			fields:  encodedOf(orderState{ID: quotedID}),
			idField: 1,
			want:    regexp.MustCompile("^" + regexp.QuoteMeta(quotedID) + "$"),
		},
		{
			name:    "[Success] When: idField is -1 ; Should: return a random 32-char hex ID",
			fields:  encodedOf(tripState{Hotel: "x"}),
			idField: -1,
			want:    hexID,
		},
		{
			name:    "[Fail] When: the id field does not hold a JSON string ; Should: return ErrInvalidState",
			fields:  func() ([][]byte, error) { return [][]byte{[]byte(`"x"`), []byte(`42`)}, nil },
			idField: 1,
			wantErr: ErrInvalidState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fields, err := tt.fields()
			require.NoError(t, err)

			got, err := executionOf(fields, tt.idField)
			require.ErrorIs(t, err, tt.wantErr)

			if tt.want == nil {
				assert.Empty(t, got)

				return
			}

			assert.Regexp(t, tt.want, got)
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
		name       string
		script     map[int]error
		appends    int
		failAt     int
		wantStored int
		wantCalls  int
		wantErrs   []error
		wantNoErrs []error
		wantCauses []error
	}{
		{
			name:       "[Success] When: the first event is appended ; Should: give it Seq 1 and the run's Writer",
			appends:    1,
			wantStored: 1,
			wantCalls:  1,
		},
		{
			name:       "[Success] When: two events are appended ; Should: number them 1 and 2",
			appends:    2,
			wantStored: 2,
			wantCalls:  2,
		},
		{
			name: "[Fail] When: the journal returns a plain error ; " +
				"Should: wrap ErrJournal, keep seq and resend the same event before the next one",
			script:     map[int]error{1: errAlwaysFails},
			appends:    2,
			failAt:     1,
			wantStored: 2,
			wantCalls:  3,
			wantErrs:   []error{ErrJournal, errAlwaysFails},
			wantNoErrs: []error{ErrConcurrentWriter, ErrExecutionExists},
			wantCauses: []error{ErrJournal},
		},
		{
			name: "[Fail] When: the journal stores an event, time passes and it returns an error ; " +
				"Should: resend the equal event before the next one, not end ErrConcurrentWriter",
			script:     map[int]error{1: errStoreThenFail},
			appends:    2,
			failAt:     1,
			wantStored: 2,
			wantCalls:  3,
			wantErrs:   []error{ErrJournal, errStoreThenFail},
			wantNoErrs: []error{ErrConcurrentWriter, ErrExecutionExists},
			wantCauses: []error{ErrJournal},
		},
		{
			name:       "[Fail] When: the journal rejects Seq 1 ; Should: return ErrExecutionExists as is and keep seq",
			script:     map[int]error{1: fmt.Errorf("%w: exec-1", ErrExecutionExists)},
			appends:    2,
			failAt:     1,
			wantStored: 1,
			wantCalls:  2,
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
			wantCalls:  3,
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
				changed := []string{hotelField}
				err = r.append(ctx, &Event{Kind: EventStepSucceeded, Step: "a", Changed: changed})
				changed[0] = "mutated"

				wantErrs := tt.wantErrs
				if i != tt.failAt {
					wantErrs = nil
				}

				requireAppendErr(t, err, wantErrs, tt.wantNoErrs)
			}

			assertAppended(t, j, tt.wantStored, tt.wantCalls)

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

func assertAppended(t *testing.T, j *scriptJournal, wantStored, calls int) {
	t.Helper()

	events := storedEvents(t, j.MemoryJournal, "exec-1")
	require.Len(t, events, wantStored)
	require.Len(t, j.ctxs, calls)

	for i := range events {
		e := &events[i]
		assert.Equal(t, i+1, e.Seq)
		assert.Equal(t, "trip", e.Saga)
		assert.Equal(t, "exec-1", e.Execution)
		assert.Equal(t, "writer-1", e.Writer)
		assert.Equal(t, EventVersion, e.Version)
		assert.Equal(t, EventStepSucceeded, e.Kind)
		assert.Equal(t, []string{hotelField}, e.Changed)
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

func (r *recorder) do(name string) func(context.Context, tripState) error {
	return func(context.Context, tripState) error {
		r.add(name)

		return nil
	}
}

func (r *recorder) book(name string, hotel Hotel) func(context.Context, tripState) (Hotel, error) {
	return func(context.Context, tripState) (Hotel, error) {
		r.add(name)

		return hotel, nil
	}
}

func (r *recorder) saw(name string) func(context.Context, tripState) error {
	return func(_ context.Context, s tripState) error {
		r.add(name + " saw " + string(s.Hotel))

		return nil
	}
}

func (r *recorder) step(name string) *actStep { return act(name, r.do(name)) }

func (r *recorder) booking(name string) *makeStep {
	return producing(name, r.book(name, hotelRitz))
}

func (r *recorder) stepIf(name string, pred func(context.Context, tripState) bool) *ifStep {
	return gated(name, r.do(name), pred)
}

func (r *recorder) bookingIf(name string, hotel Hotel, pred func(context.Context, tripState) bool) *ifMakeStep {
	return &ifMakeStep{Name: name, make: r.book(name, hotel), pred: pred}
}

func (r *recorder) pivot() *pivotStep { return paying("p", r.do("p")) }

func (r *recorder) when(verdict bool, name string) func(context.Context, tripState) bool {
	return func(context.Context, tripState) bool {
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
	serial             bool
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
	wantClosed         map[string]EventError
	wantTextCount      map[string]int
	wantErrText        map[string]string
	wantUnrecorded     []string
	wantSteps          map[string]stepStatus
	wantPending        []string
	wantCompensated    []string
	wantJournalOnly    []string
	wantRunOnly        []string
	wantOrder          [][2]string
	wantZones          []StepZone
	wantTrail          [][]string
	wantChanged        map[string][]string
	wantEventAttempts  map[string]int
	initial            tripState
	wantState          tripState
}

func Test_Saga_Run(t *testing.T) {
	t.Parallel()

	for _, tt := range slices.Concat(
		forwardRows(), pivotRows(), stopRows(), errorRows(), backwardRows(), runRows(), resendRows(),
	) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				rec := &recorder{}
				journal := newScriptJournal(tt.script)

				s, err := New[tripState]("trip", tt.steps(rec), append(slices.Clone(tt.opts), WithJournal(journal))...)
				require.NoError(t, err)

				runs := execute(s, &tt)
				results := journal.results(t)
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

	var pending, failed, compensated []string

	if tt.wantKind == nil {
		require.NoError(t, err)
	} else {
		runErr, ok := errors.AsType[*RunError](err)
		require.True(t, ok, "a failed run is not a RunError: %v", err)
		assert.Same(t, tt.wantKind, runErr.Kind)
		require.ErrorIs(t, err, tt.wantKind)
		assertExclusive(t, err)

		pending, compensated = runErr.Pending, runErr.Compensated

		for _, se := range runErr.Steps {
			failed = append(failed, keyOf(se))
		}
	}

	assert.Equal(t, tt.wantFailed, failed)
	assert.Equal(t, tt.wantCompensated, compensated)

	assert.Equal(t, tt.wantStatus, res.Status)
	assert.Equal(t, endKindOf(tt.wantStatus), res.End)
	assert.Equal(t, tt.wantPending, pending)
	assert.Equal(t, journalPending(res, tt), pendingOf(res.events))
	assert.Equal(t, tt.wantState, got.state)
	assertSteps(t, res, tt.wantSteps)
	assertEvents(t, res, tt, storedEvents(t, journal.MemoryJournal, res.Execution))

	assertFailures(t, tt, got, res)
}

func endKindOf(status Status) EventKind {
	switch status {
	case StatusCompleted, StatusCompensated:
		return EventSagaFinished
	case StatusCompensationFailed, StatusIncomplete:
		return EventSagaSuspended
	case noEndStatus:
		return ""
	}

	return ""
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

			if tt.serial {
				ctx = serial.With(ctx)
			}

			out[i].state = tt.initial
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
		assert.Equal(t, EventVersion, events[i].Version)
		assert.Equal(t, res.Execution, events[i].Execution)

		if events[i].Kind == EventStepStarted {
			started[events[i].Step] = true
		}

		key := trailOf(events[i].Kind, events[i].Step)
		trail = append(trail, key)

		if want, found := tt.wantChanged[key]; found {
			assert.Equal(t, want, events[i].Changed, "event %s", key)
		}

		if want, found := tt.wantEventAttempts[key]; found {
			assert.Equal(t, want, events[i].Attempts, "event %s", key)
		}
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

func randomExecutionRow() runRow {
	return runRow{
		name: "[Success] When: two runs of a state without an id tag ; Should: complete both under two random IDs",
		steps: func(rec *recorder) Flow {
			return rec.step("a")
		},
		runs:       2,
		wantStatus: StatusCompleted,
		wantTrail:  seq(opened, startOf("a"), doneOf("a"), closed),
		wantSteps:  map[string]stepStatus{"a": stepSucceeded},
	}
}

func runRows() []runRow {
	return []runRow{
		randomExecutionRow(),
		finishedAfterCompensatedRow(),
		finishedAfterCompensationFailedRow(),
		finishedAfterIncompleteRow(),
		pivotOutcomeAppendRow(),
		pivotOutcomeRejectedRow(),
		forwardOnlyIntentRow(),
		undoIntentRejectedRow(),
		undoRunningRejectedRow(),
		undoOutcomeRejectedRow(),
		undoneThenRejectedRow(),
		turnAfterRejectionRow(),
		forwardIntentAtGraceEndRow(),
		pivotOutcomeAtGraceEndRow(),
		finishedAtGraceEndRow(),
		completedFinishAtGraceEndRow(),
		completedMixedFinishAtGraceEndRow(),
		finishedBeforeGraceEndRow(),
		skippedAtGraceEndRow(),
		lateForwardOnlyVerdictRow(),
		lateCompensableVerdictRow(),
		parallelFailuresRow(),
		compensatedListRow(),
	}
}

func (r *recorder) lateYes(name string) func(context.Context, tripState) bool {
	return func(context.Context, tripState) bool {
		time.Sleep(pause)
		r.add("if:" + name)

		return true
	}
}

func lateForwardOnlyVerdictRow() runRow {
	return runRow{
		name: "[Fail] When: a forward-only Enabled method says yes after a sibling failed ; " +
			"Should: not start the step and end ErrIncomplete",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.pivot(), Parallel(once("y", alwaysFails), rec.stepIf("z", rec.lateYes("z"))))
		},
		wantStatus:   StatusIncomplete,
		wantKind:     ErrIncomplete,
		wantKinds:    map[string]*KindError{"y": ErrFailed},
		wantFailed:   []string{"y"},
		wantErrText:  map[string]string{"y": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"y": 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{context.Canceled},
		wantSteps:    map[string]stepStatus{"p": stepSucceeded, "y": stepFailed, "z": stepNotStarted},
		wantPending:  []string{"y", "z"},
		wantOrder:    [][2]string{{"p", "if:z"}},
		wantTrail:    seq(opened, startOf("p"), doneOf("p"), startOf("y"), failedOf("y"), suspended),
		wantZones: []StepZone{
			{Name: "p", Zone: ZonePivot}, {Name: "y", Zone: ZoneForwardOnly}, {Name: "z", Zone: ZoneForwardOnly},
		},
	}
}

func lateCompensableVerdictRow() runRow {
	return runRow{
		name: "[Fail] When: a compensable Enabled method says yes after a sibling failed ; " +
			"Should: not start the step and not read the stop as a caller cancel",
		steps: func(rec *recorder) Flow {
			return Sequence(Parallel(once("a", alwaysFails), rec.stepIf("b", rec.lateYes("b"))), rec.pivot())
		},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"a": ErrFailed},
		wantFailed:   []string{"a"},
		wantErrText:  map[string]string{"a": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"a": 1},
		wantErrs:     []error{errAlwaysFails},
		wantNotErrs:  []error{context.Canceled},
		wantSteps:    map[string]stepStatus{"a": stepFailed, "b": stepNotStarted, "p": stepNotStarted},
		wantTrail:    seq(opened, startOf("a"), failedOf("a"), closed),
		wantZones: []StepZone{
			{Name: "a", Zone: ZoneCompensable}, {Name: "b", Zone: ZoneCompensable}, {Name: "p", Zone: ZonePivot},
		},
	}
}

func skippedAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: the step-skipped append of a forward-only step fails at the grace end ; " +
			"Should: end ErrIncomplete and match ErrGraceEnded and the caller cause, not ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), rec.stepIf("z", rec.when(false, "z")))
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		script:      map[int]error{6: errAwaitDone},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  noEndStatus,
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
			"Should: end ErrIncomplete and match ErrGraceEnded and the caller cause, not ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), rec.step("z"))
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		script:      map[int]error{6: errAwaitDone},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  noEndStatus,
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
			"Should: leave the pivot unknown and match ErrGraceEnded and the caller cause, not ErrJournal",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), rec.step("z"))
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		script:      map[int]error{5: errAwaitDone},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  noEndStatus,
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
			"Should: keep ErrCompensated and match ErrGraceEnded and the caller cause, not ErrJournal",
		wantCompensated: []string{"a"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), once("b", alwaysFails))
		},
		opts:         []Option{WithRecoveryTimeout(pause)},
		script:       map[int]error{8: errAwaitDone},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   noEndStatus,
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

func completedFinishAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: every step succeeded and the grace end refuses the saga-finished append ; " +
			"Should: end ErrGraceEnded and match the caller cause, not ErrJournal",
		steps: func(rec *recorder) Flow {
			return rec.step("a")
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		script:      map[int]error{3: errAwaitThenKeep},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  noEndStatus,
		wantKind:    ErrGraceEnded,
		wantErrs:    []error{ErrGraceEnded, errStop},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded},
		wantTrail:   seq(opened, startOf("a"), doneOf("a")),
	}
}

func completedMixedFinishAtGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: every step succeeded and the saga-finished append fails for real after the grace end ; " +
			"Should: end ErrJournal and match the journal error and ErrGraceEnded",
		steps: func(rec *recorder) Flow {
			return rec.step("a")
		},
		opts:        []Option{WithRecoveryTimeout(pause)},
		script:      map[int]error{3: errAwaitThenKeep, 4: errors.Join(errJournalDown, context.Canceled)},
		cancelAfter: pause,
		cancelCause: errStop,
		wantStatus:  noEndStatus,
		wantKind:    ErrJournal,
		wantErrs:    []error{ErrJournal, errJournalDown, ErrGraceEnded, errStop},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded},
		wantTrail:   seq(opened, startOf("a"), doneOf("a")),
	}
}

func finishedBeforeGraceEndRow() runRow {
	return runRow{
		name: "[Fail] When: the saga-finished append fails after a caller cancel and before any grace end ; " +
			"Should: keep ErrCompensated and match neither ErrGraceEnded nor the caller cause",
		wantCompensated: []string{"a"},
		steps: func(rec *recorder) Flow {
			slowUndo := func(context.Context, tripState) error {
				time.Sleep(pause)
				rec.add(undoA)

				return nil
			}

			return Sequence(undoing("a", rec.do("a"), slowUndo), once("b", alwaysFails))
		},
		script:       map[int]error{8: errJournalDown},
		cancelAfter:  pause / 2,
		cancelCause:  errStop,
		wantStatus:   noEndStatus,
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
		wantCompensated: []string{"a"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), once("b", alwaysFails))
		},
		script:       map[int]error{8: errJournalDown},
		wantStatus:   noEndStatus,
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
		wantStatus:   noEndStatus,
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
	zones = append(zones,
		StepZone{Name: "a", Zone: ZoneCompensable, Compensable: true}, StepZone{Name: "p", Zone: ZonePivot},
	)

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
		wantStatus:   noEndStatus,
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
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepNotStarted},
		wantPending: []string{"p", "z"},
		wantRunOnly: []string{"p"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), suspended),
		wantZones:   pivotZones("z"),
	}
}

func pivotOutcomeRejectedRow() runRow {
	return runRow{
		name: "[Fail] When: another writer rejects the outcome append of a successful pivot ; " +
			"Should: end ErrConcurrentWriter and list the pivot and the forward-only step in Pending",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.pivot(), rec.step("z"))
		},
		script:      map[int]error{5: ErrConcurrentWriter},
		wantStatus:  noEndStatus,
		wantKind:    ErrConcurrentWriter,
		wantErrs:    []error{ErrConcurrentWriter},
		wantNotErrs: []error{ErrJournal},
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepUnknown, "z": stepNotStarted},
		wantPending: []string{"p", "z"},
		wantRunOnly: []string{"z"},
		wantTrail:   seq(opened, startOf("a"), doneOf("a"), startOf("p")),
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
		wantSteps:   map[string]stepStatus{"a": stepSucceeded, "p": stepSucceeded, "z": stepFailed},
		wantPending: []string{"z"},
		wantClosed:  map[string]EventError{"z": journalDownEvent()},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), doneOf("p"), startOf("z"), failedOf("z"), suspended,
		),
		wantZones: pivotZones("z"),
	}
}

func undoIntentRejectedRow() runRow {
	return runRow{
		name: "[Fail] When: the journal rejects a compensation intent ; " +
			"Should: end ErrConcurrentWriter, fail no compensation and list the unstarted ones in Pending",
		steps: func(rec *recorder) Flow {
			b := failingUndo("b", Continue, rec.do(undoB))

			return Sequence(rec.undoable("a"), b, once("c", alwaysFails))
		},
		script:       map[int]error{8: fmt.Errorf("%w: other writer", ErrConcurrentWriter)},
		wantStatus:   noEndStatus,
		wantKind:     ErrConcurrentWriter,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{ErrConcurrentWriter, errAlwaysFails},
		wantNotErrs:  []error{ErrJournal},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded, "c": stepFailed},
		wantPending:  []string{"a", "b"},
		wantRunOnly:  []string{"a", "b"},
		wantTrail:    chainTrail(),
	}
}

func rejectedUndoFlow(d Flow) func(*recorder) Flow {
	return func(rec *recorder) Flow {
		return Sequence(Parallel(
			Sequence(rec.undoable("x"), rec.undoable("b")),
			Sequence(rec.undoable("y"), d),
		), once("c", alwaysFails))
	}
}

func lateUndoneHotel() *replaceStep {
	d := replaceOnce("d", makeOf[Hotel](""), func(context.Context, tripState) (Hotel, error) {
		time.Sleep(pause)

		return undoneHotel, nil
	})
	d.FailurePolicy = Continue

	return d
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
			"Should: end ErrConcurrentWriter without failing it and list it and the unstarted one in Pending",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"), once("c", alwaysFails))
		},
		script:       map[int]error{9: ErrConcurrentWriter},
		wantStatus:   noEndStatus,
		wantKind:     ErrConcurrentWriter,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{ErrConcurrentWriter, errAlwaysFails},
		wantNotErrs:  []error{ErrJournal},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepCompensationFailed, "c": stepFailed},
		wantPending:  []string{"a", "b"},
		wantRunOnly:  []string{"a"},
		wantOrder:    [][2]string{{"b", undoB}},
		wantTrail:    chainTrail(compStartOf("b")),
	}
}

func undoneThenRejectedRow() runRow {
	return runRow{
		name: "[Fail] When: the journal rejects a compensation intent after another compensation is recorded ; " +
			"Should: end ErrConcurrentWriter and list only the unstarted compensation in Pending",
		wantCompensated: []string{"b"},
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"), once("c", alwaysFails))
		},
		script:       map[int]error{10: ErrConcurrentWriter},
		wantStatus:   noEndStatus,
		wantKind:     ErrConcurrentWriter,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantErrs:     []error{ErrConcurrentWriter, errAlwaysFails},
		wantNotErrs:  []error{ErrJournal},
		wantSteps:    map[string]stepStatus{"a": stepSucceeded, "b": stepCompensated, "c": stepFailed},
		wantPending:  []string{"a"},
		wantRunOnly:  []string{"a"},
		wantOrder:    [][2]string{{"b", undoB}},
		wantTrail:    chainTrail(compStartOf("b"), undoneOf("b")),
	}
}

func undoRunningRejectedRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation succeeds after the journal rejected an event ; " +
			"Should: commit and append nothing for it, and list it and the unstarted ones in Pending",
		steps:        rejectedUndoFlow(lateUndoneHotel()),
		script:       map[int]error{13: ErrConcurrentWriter},
		wantStatus:   noEndStatus,
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
		wantPending: []string{"x", "b", "y", "d"},
		wantRunOnly: []string{"x", "b", "y"},
		wantTrail:   rejectedUndoTrail(),
	}
}

func turnAfterRejectionRow() runRow {
	return runRow{
		name: "[Fail] When: a compensation's turn comes after a rejection and the grace end ; " +
			"Should: neither run nor fail it, and list the unstarted compensations in Pending",
		steps:        rejectedUndoFlow(failingUndo("d", Continue, lateFailure)),
		opts:         []Option{WithRecoveryTimeout(pause)},
		script:       map[int]error{13: ErrConcurrentWriter},
		cancelAfter:  pause,
		cancelCause:  errStop,
		wantStatus:   noEndStatus,
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
		wantPending:    []string{"x", "b", "y", "d"},
		wantRunOnly:    []string{"x", "b", "y"},
		wantUnrecorded: []string{comp("d")},
		wantTrail:      rejectedUndoTrail(),
	}
}

func Test_endKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     Status
		rejected   bool
		appendKind *KindError
		want       *KindError
	}{
		{
			name:   "[Success] When: every started compensation succeeded ; Should: return ErrCompensated",
			status: StatusCompensated,
			want:   ErrCompensated,
		},
		{
			name:       "[Success] When: a compensated run also had a journal failure ; Should: keep ErrCompensated",
			status:     StatusCompensated,
			appendKind: ErrJournal,
			want:       ErrCompensated,
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
			name:       "[Success] When: every step succeeded and the journal failed ; Should: return ErrJournal",
			status:     StatusCompleted,
			appendKind: ErrJournal,
			want:       ErrJournal,
		},
		{
			name: "[Success] When: every step succeeded and only the grace end refused an append ; " +
				"Should: return ErrGraceEnded",
			status:     StatusCompleted,
			appendKind: ErrGraceEnded,
			want:       ErrGraceEnded,
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

			assert.Same(t, tt.want, endKind(tt.status, tt.rejected, tt.appendKind))
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

func Benchmark_Saga_Run_producing(b *testing.B) {
	s, err := New[tripState]("trip", Sequence(producing("a", makeHotel), producingRate("b", makeRate)))
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

type emptyBatchError struct{}

func (emptyBatchError) Error() string { return "batch write failed" }

func (emptyBatchError) Unwrap() []error { return nil }

type canceledLeafError struct{}

func (canceledLeafError) Error() string { return "append canceled" }

func (canceledLeafError) Unwrap() error { return nil }

func (canceledLeafError) Is(target error) bool { return target == context.Canceled }

func Test_ownError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  func(ctx context.Context) error
		want bool
	}{
		{
			name: "[Success] When: the error is the context's error ; Should: return true",
			err:  func(ctx context.Context) error { return ctx.Err() },
			want: true,
		},
		{
			name: "[Success] When: the error is the context's cause ; Should: return true",
			err:  context.Cause,
			want: true,
		},
		{
			name: "[Success] When: a wrapper holds the context's error ; Should: return true",
			err:  func(ctx context.Context) error { return fmt.Errorf("append: %w", ctx.Err()) },
			want: true,
		},
		{
			name: "[Success] When: a join holds only the context's error and its cause ; Should: return true",
			err:  func(ctx context.Context) error { return errors.Join(ctx.Err(), context.Cause(ctx)) },
			want: true,
		},
		{
			name: "[Fail] When: a join holds a journal failure and the context's error ; Should: return false",
			err:  func(ctx context.Context) error { return errors.Join(errJournalDown, ctx.Err()) },
			want: false,
		},
		{
			name: "[Fail] When: a nested join hides a journal failure beside the cause ; Should: return false",
			err: func(ctx context.Context) error {
				return fmt.Errorf("append: %w", errors.Join(context.Cause(ctx), fmt.Errorf("io: %w", errJournalDown)))
			},
			want: false,
		},
		{
			name: "[Success] When: a wrapper with no inner error matches the context's error itself ; " +
				"Should: return true",
			err:  func(context.Context) error { return canceledLeafError{} },
			want: true,
		},
		{
			name: "[Fail] When: a multi-error with an empty list holds only its own message ; Should: return false",
			err:  func(context.Context) error { return emptyBatchError{} },
			want: false,
		},
		{
			name: "[Fail] When: the error is a journal failure ; Should: return false",
			err:  func(context.Context) error { return errJournalDown },
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(ErrGraceEnded)

			assert.Equal(t, tt.want, ownError(ctx, tt.err(ctx)))
		})
	}
}

func resendRows() []runRow {
	return []runRow{
		pivotStoredFailureRow(),
		forwardStoredOutcomeRow(),
		backwardStoredIntentRow(),
		storedThenDownRow(),
	}
}

func pivotStoredFailureRow() runRow {
	return runRow{
		name: "[Fail] When: the journal stores the pivot failure and then returns an error ; " +
			"Should: resend the same event, compensate and end ErrCompensated, not ErrConcurrentWriter",
		wantCompensated: []string{"a"},
		steps:           failingPivot(),
		script:          map[int]error{5: errStoreThenFail},
		wantStatus:      StatusCompensated,
		wantKind:        ErrCompensated,
		wantKinds:       map[string]*KindError{"p": ErrFailed},
		wantFailed:      []string{"p"},
		wantErrText:     map[string]string{"p": errAlwaysFails.Error()},
		wantAttempts:    map[string]int{"p": 1},
		wantErrs:        []error{errAlwaysFails, ErrJournal, errStoreThenFail},
		wantNotErrs:     []error{ErrIncomplete, ErrConcurrentWriter},
		wantSteps:       map[string]stepStatus{"a": stepCompensated, "p": stepFailed},
		wantOrder:       [][2]string{{"a", undoA}},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("p"), failedOf("p"), compStartOf("a"), undoneOf("a"), closed,
		),
		wantZones: []StepZone{{Name: "a", Zone: ZoneCompensable, Compensable: true}, {Name: "p", Zone: ZonePivot}},
	}
}

func forwardStoredOutcomeRow() runRow {
	return runRow{
		name: "[Fail] When: the journal stores the outcome of a compensable step and then returns an error, and a " +
			"sibling fails later ; Should: resend the outcome first and compensate, not end ErrConcurrentWriter",
		wantCompensated: []string{"a", "b"},
		steps: func(rec *recorder) Flow {
			c := once("c", func(context.Context, tripState) error {
				time.Sleep(pause)

				return errAlwaysFails
			})

			return Sequence(rec.undoable("a"), Parallel(rec.undoable("b"), c))
		},
		script:       map[int]error{6: errStoreThenFail},
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"c": ErrFailed},
		wantFailed:   []string{"c"},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1},
		wantCauseIs:  map[string]error{"c": errJournalStopped},
		wantErrs:     []error{errAlwaysFails, ErrJournal, errStoreThenFail},
		wantNotErrs:  []error{ErrConcurrentWriter},
		wantSteps:    map[string]stepStatus{"a": stepCompensated, "b": stepCompensated, "c": stepFailed},
		wantOrder:    [][2]string{{"b", undoB}, {undoB, undoA}},
		wantTrail: slices.Concat(
			seq(opened, startOf("a"), doneOf("a")),
			together(startOf("b"), startOf("c")),
			seq(doneOf("b"), failedOf("c"), compStartOf("b"), undoneOf("b"), compStartOf("a"), undoneOf("a"), closed),
		),
	}
}

func backwardStoredIntentRow() runRow {
	return runRow{
		name: "[Fail] When: the journal stores a compensation intent and then returns an error ; " +
			"Should: resend the intent first and record the next compensation, not end ErrConcurrentWriter",
		wantCompensated: []string{"a"},
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoable("a"), failingUndo("b", Continue, rec.do(undoB)), once("c", alwaysFails),
			)
		},
		script:       map[int]error{8: errStoreThenFail},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{comp("b"): ErrJournal, "c": ErrFailed},
		wantFailed:   []string{"c", comp("b")},
		wantErrText:  map[string]string{comp("b"): errStoreThenFail.Error(), "c": errAlwaysFails.Error()},
		wantAttempts: map[string]int{comp("b"): 0, "c": 1},
		wantErrs:     []error{ErrJournal, errStoreThenFail},
		wantNotErrs:  []error{ErrConcurrentWriter},
		wantSteps: map[string]stepStatus{
			"a": stepCompensated, "b": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"b"},
		wantOrder:   [][2]string{{"a", undoA}},
		wantTrail:   chainTrail(compStartOf("b"), undoFailedOf("b"), compStartOf("a"), undoneOf("a"), suspended),
	}
}

func storedThenDownRow() runRow {
	return runRow{
		name: "[Fail] When: the journal stores a step outcome and then stays down ; " +
			"Should: leave the step unknown, fail its compensation with ErrJournal and not end ErrConcurrentWriter",
		steps: func(rec *recorder) Flow {
			return Sequence(rec.undoable("a"), rec.undoable("b"))
		},
		script:         map[int]error{5: failing(errStoreThenFail, forever)},
		wantStatus:     noEndStatus,
		wantKind:       ErrCompensationFailed,
		wantKinds:      map[string]*KindError{comp("b"): ErrJournal},
		wantFailed:     []string{comp("b")},
		wantErrText:    map[string]string{comp("b"): errStoreThenFail.Error()},
		wantAttempts:   map[string]int{comp("b"): 0},
		wantUnrecorded: []string{comp("b")},
		wantErrs:       []error{ErrJournal, errStoreThenFail},
		wantNotErrs:    []error{ErrConcurrentWriter},
		wantSteps:      map[string]stepStatus{"a": stepSucceeded, "b": stepSucceeded},
		wantPending:    []string{"a", "b"},
		wantRunOnly:    []string{"a", "b"},
		wantTrail:      seq(opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b")),
	}
}

func parallelFailuresRow() runRow {
	return runRow{
		name:         "[Success] When: two parallel steps fail ; Should: Steps lists them in step order",
		steps:        func(*recorder) Flow { return Parallel(once("a", failsAfter(pause)), once("b", alwaysFails)) },
		runs:         50,
		wantStatus:   StatusCompensated,
		wantKind:     ErrCompensated,
		wantKinds:    map[string]*KindError{"a": ErrFailed, "b": ErrFailed},
		wantFailed:   []string{"a", "b"},
		wantErrText:  map[string]string{"a": errAlwaysFails.Error(), "b": errAlwaysFails.Error()},
		wantAttempts: map[string]int{"a": 1, "b": 1},
		wantCauseIs:  map[string]error{"a": errStepStopped},
		wantErrs:     []error{errAlwaysFails},
		wantSteps:    map[string]stepStatus{"a": stepFailed, "b": stepFailed},
		wantTrail: slices.Concat(
			seq(opened), together(startOf("a"), startOf("b")), seq(failedOf("b"), failedOf("a"), closed),
		),
	}
}

func compensatedListRow() runRow {
	return runRow{
		name: "[Success] When: a step fails and two compensations succeed, one fails ; " +
			"Should: Compensated lists the two in step order " +
			"and Steps lists the action error then the compensation error",
		wantCompensated: []string{"a", "b"},
		steps: func(rec *recorder) Flow {
			return Sequence(
				rec.undoable("a"), rec.undoable("b"), failingUndo("d", Continue, alwaysFails), once("c", alwaysFails),
			)
		},
		wantStatus:   StatusCompensationFailed,
		wantKind:     ErrCompensationFailed,
		wantKinds:    map[string]*KindError{"c": ErrFailed, comp("d"): ErrFailed},
		wantFailed:   []string{"c", comp("d")},
		wantErrText:  map[string]string{"c": errAlwaysFails.Error(), comp("d"): errAlwaysFails.Error()},
		wantAttempts: map[string]int{"c": 1, comp("d"): 1},
		wantErrs:     []error{errAlwaysFails},
		wantSteps: map[string]stepStatus{
			"a": stepCompensated, "b": stepCompensated, "d": stepCompensationFailed, "c": stepFailed,
		},
		wantPending: []string{"d"},
		wantTrail: seq(
			opened, startOf("a"), doneOf("a"), startOf("b"), doneOf("b"), startOf("d"), doneOf("d"),
			startOf("c"), failedOf("c"), compStartOf("d"), undoFailedOf("d"), compStartOf("b"), undoneOf("b"),
			compStartOf("a"), undoneOf("a"), suspended,
		),
	}
}
